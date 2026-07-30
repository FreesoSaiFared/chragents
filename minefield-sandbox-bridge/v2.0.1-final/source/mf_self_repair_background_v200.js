(() => {
  'use strict';

  const VERSION = '2.0.1';
  const API = 'http://127.0.0.1:9789';
  const BROKER_TOKEN = '__MF_BROKER_TOKEN__';
  const CONTINUATION_ALARM = 'mf-total-control-continuation-v200';
  const CONTROL_STATE_KEY = 'mfTotalControlV200State';
  const ALARM = 'mf-selfrepair-v200';
  const STORAGE_STATE = 'mfSelfRepairV200State';
  const STORAGE_REPORT = 'mfSelfRepairV200LastReport';
  const STORAGE_QUARANTINE = 'mfSelfRepairV200Quarantine';
  const CHAT_PATTERNS = ['https://chatgpt.com/*', 'https://chat.openai.com/*'];
  const MAX_REPAIRS_PER_PASS = 5;
  const REPAIR_COOLDOWN_MS = 120000;
  const FRESH_HEARTBEAT_MS = 20000;

  if (globalThis.__mfSelfRepairBackgroundV200) return;
  globalThis.__mfSelfRepairBackgroundV200 = {version: VERSION, loadedAt: Date.now()};

  function nowIso() { return new Date().toISOString(); }
  function sleep(ms) { return new Promise(resolve => setTimeout(resolve, ms)); }
  function asArray(value) { return Array.isArray(value) ? value : []; }
  function safeClone(value) {
    try { return JSON.parse(JSON.stringify(value)); } catch (_) { return null; }
  }

  function tabKey(tabId) { return String(tabId); }

  function heartbeatTimestamp(heartbeat) {
    if (!heartbeat || typeof heartbeat !== 'object') return 0;
    const candidates = [
      heartbeat.at,
      heartbeat.ts,
      heartbeat.timestamp,
      heartbeat.updatedAt,
      heartbeat.lastSeenAt,
      heartbeat.lastHeartbeatAt,
      heartbeat.heartbeatAt
    ];
    for (const candidate of candidates) {
      const number = typeof candidate === 'number' ? candidate : Date.parse(candidate || '');
      if (Number.isFinite(number) && number > 0) return number;
    }
    return 0;
  }

  async function sha256(text) {
    const bytes = new TextEncoder().encode(text);
    const digest = await crypto.subtle.digest('SHA-256', bytes);
    return Array.from(new Uint8Array(digest)).map(v => v.toString(16).padStart(2, '0')).join('');
  }

  function stableJson(value) {
    if (value === null || typeof value !== 'object') return JSON.stringify(value);
    if (Array.isArray(value)) return `[${value.map(stableJson).join(',')}]`;
    return `{${Object.keys(value).sort().map(k => `${JSON.stringify(k)}:${stableJson(value[k])}`).join(',')}}`;
  }

  async function pendingIdentity(item) {
    if (!item || typeof item !== 'object') return `scalar:${String(item)}`;
    const explicit = [
      item.returnTicket,
      item.ticketId,
      item.originTicket,
      item.resultId,
      item.operationId,
      item.id,
      item.downloadId
    ].find(v => typeof v === 'string' && v.length > 3);
    if (explicit) return `id:${explicit}`;
    const origin = item.conversationId || item.originConversationId || item.chatId || item.url || '';
    const payload = item.payload ?? item.result ?? item.content ?? item.text ?? item;
    return `hash:${origin}:${await sha256(stableJson(payload))}`;
  }

  async function dedupePendingReturns(allStorage) {
    const changes = {};
    const quarantine = [];
    let duplicates = 0;
    const existingQuarantine = asArray(allStorage[STORAGE_QUARANTINE]);

    for (const [key, value] of Object.entries(allStorage)) {
      if (!/pending/i.test(key) || !/(return|result|delivery)/i.test(key) || !Array.isArray(value)) continue;
      const seen = new Set();
      const kept = [];
      for (const item of value) {
        const identity = await pendingIdentity(item);
        if (seen.has(identity)) {
          duplicates += 1;
          quarantine.push({sourceKey: key, identity, quarantinedAt: nowIso(), item: safeClone(item)});
        } else {
          seen.add(identity);
          kept.push(item);
        }
      }
      if (kept.length !== value.length) changes[key] = kept;
    }

    if (quarantine.length) {
      changes[STORAGE_QUARANTINE] = [...existingQuarantine, ...quarantine].slice(-1000);
      await chrome.storage.local.set(changes);
    }
    return {duplicates, changedKeys: Object.keys(changes).filter(k => k !== STORAGE_QUARANTINE), quarantined: quarantine.length};
  }

  async function broker(path, options = {}) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), options.timeout || 5000);
    try {
      const response = await fetch(`${API}${path}`, {
        method: options.method || 'GET',
        headers: {'content-type': 'application/json', 'x-minefield-token': BROKER_TOKEN},
        body: options.body ? JSON.stringify(options.body) : undefined,
        signal: controller.signal
      });
      const text = await response.text();
      let body;
      try { body = JSON.parse(text); } catch (_) { body = {raw: text}; }
      return {ok: response.ok, status: response.status, body};
    } catch (error) {
      return {ok: false, error: String(error?.message || error)};
    } finally {
      clearTimeout(timeout);
    }
  }

  async function getState() {
    const data = await chrome.storage.local.get([STORAGE_STATE]);
    return data[STORAGE_STATE] || {tabs: {}, incidents: [], lastRunAt: null};
  }

  async function setState(state) {
    await chrome.storage.local.set({[STORAGE_STATE]: state});
  }

  async function pingTab(tabId) {
    try {
      const response = await chrome.tabs.sendMessage(tabId, {kind: 'mf.selfrepair.probe'});
      return response && response.ok ? response : null;
    } catch (_) {
      return null;
    }
  }

  function contentBundlesForUrl(url) {
    const manifest = chrome.runtime.getManifest();
    const result = [];
    for (const item of manifest.content_scripts || []) {
      const matches = asArray(item.matches);
      const relevant = matches.some(pattern => {
        if (pattern.includes('chatgpt.com') && url.includes('chatgpt.com')) return true;
        if (pattern.includes('chat.openai.com') && url.includes('chat.openai.com')) return true;
        return pattern === '<all_urls>';
      });
      if (!relevant) continue;
      const js = asArray(item.js).filter(file => file !== 'mf_contract_core_v200.js' && file !== 'mf_self_repair_content_v200.js');
      if (js.length) result.push(js);
    }
    return result;
  }

  async function injectFiles(tabId, files) {
    if (!chrome.scripting?.executeScript) return {ok: false, error: 'SCRIPTING_API_UNAVAILABLE'};
    try {
      await chrome.scripting.executeScript({target: {tabId}, files});
      return {ok: true, files};
    } catch (error) {
      return {ok: false, error: String(error?.message || error), files};
    }
  }

  async function repairTab(tab, legacyHeartbeat, state, reason) {
    const key = tabKey(tab.id);
    const prior = state.tabs[key] || {};
    const firstPing = await pingTab(tab.id);
    if (firstPing && Date.now() - heartbeatTimestamp(firstPing) <= FRESH_HEARTBEAT_MS) {
      state.tabs[key] = {
        ...prior,
        url: tab.url,
        generation: firstPing.generation,
        actorId: firstPing.actorId,
        lastHealthyAt: Date.now(),
        lastOutcome: 'HEALTHY'
      };
      return {tabId: tab.id, outcome: 'HEALTHY', generation: firstPing.generation};
    }

    const generationHint = firstPing?.generation || legacyHeartbeat?.generation || `${tab.url}:${tab.pendingUrl || ''}`;
    const cooldownKey = `${generationHint}`;
    if (prior.lastRepairAt && prior.lastRepairKey === cooldownKey && Date.now() - prior.lastRepairAt < REPAIR_COOLDOWN_MS) {
      return {tabId: tab.id, outcome: 'REPAIR_COOLDOWN', ageMs: Date.now() - prior.lastRepairAt};
    }

    state.tabs[key] = {...prior, url: tab.url, lastRepairAt: Date.now(), lastRepairKey: cooldownKey, lastReason: reason};

    const selfInjection = await injectFiles(tab.id, ['mf_self_repair_content_v200.js']);
    await sleep(350);
    let ping = await pingTab(tab.id);
    if (!ping) {
      for (const bundle of contentBundlesForUrl(tab.url || '')) {
        await injectFiles(tab.id, bundle);
      }
      await sleep(700);
      ping = await pingTab(tab.id);
    }

    if (ping) {
      state.tabs[key] = {
        ...state.tabs[key],
        generation: ping.generation,
        actorId: ping.actorId,
        lastHealthyAt: Date.now(),
        lastOutcome: 'REPAIRED',
        selfInjection
      };
      return {tabId: tab.id, outcome: 'REPAIRED', generation: ping.generation, selfInjection};
    }

    state.tabs[key] = {...state.tabs[key], lastOutcome: 'NEEDS_REPAIR', selfInjection};
    return {tabId: tab.id, outcome: 'NEEDS_REPAIR', selfInjection};
  }

  async function investigate(reason = 'scheduled', suppliedContent = null) {
    const startedAt = Date.now();
    const [tabs, allStorage, state, brokerHealth] = await Promise.all([
      chrome.tabs.query({url: CHAT_PATTERNS}).catch(() => []),
      chrome.storage.local.get(null),
      getState(),
      broker('/health', {timeout: 2500})
    ]);

    const legacyMap = allStorage.mfWatchdogHeartbeatsByTab || {};
    const dedupe = await dedupePendingReturns(allStorage);
    const outcomes = [];
    let repairsAttempted = 0;

    for (const tab of tabs) {
      if (!tab.id || tab.discarded) {
        outcomes.push({tabId: tab.id, outcome: tab.discarded ? 'DISCARDED' : 'INVALID_TAB'});
        continue;
      }
      const legacyHeartbeat = legacyMap[tabKey(tab.id)] || legacyMap[tab.id] || null;
      const legacyFresh = Date.now() - heartbeatTimestamp(legacyHeartbeat) <= FRESH_HEARTBEAT_MS;
      const actor = await pingTab(tab.id);
      if (actor || legacyFresh) {
        outcomes.push({tabId: tab.id, outcome: 'HEALTHY', actor: Boolean(actor), legacyFresh});
        if (actor) {
          state.tabs[tabKey(tab.id)] = {
            ...(state.tabs[tabKey(tab.id)] || {}),
            url: tab.url,
            generation: actor.generation,
            actorId: actor.actorId,
            lastHealthyAt: Date.now(),
            lastOutcome: 'HEALTHY'
          };
        }
        continue;
      }
      if (repairsAttempted >= MAX_REPAIRS_PER_PASS) {
        outcomes.push({tabId: tab.id, outcome: 'DEFERRED_BOUNDED_PASS'});
        continue;
      }
      repairsAttempted += 1;
      outcomes.push(await repairTab(tab, legacyHeartbeat, state, reason));
    }

    const unresolved = outcomes.filter(item => item.outcome === 'NEEDS_REPAIR').length;
    const repaired = outcomes.filter(item => item.outcome === 'REPAIRED').length;
    const stale = outcomes.filter(item => !['HEALTHY', 'REPAIRED'].includes(item.outcome)).length;
    const report = {
      schema: 'minefield.selfrepair.report/1',
      version: VERSION,
      reason,
      startedAt,
      completedAt: Date.now(),
      durationMs: Date.now() - startedAt,
      tabs: tabs.length,
      stale,
      repaired,
      unresolved,
      repairsAttempted,
      pendingDedupe: dedupe,
      broker: brokerHealth,
      suppliedContentPresent: Boolean(suppliedContent),
      outcomes
    };

    state.lastRunAt = Date.now();
    state.lastReport = {startedAt, reason, repaired, unresolved, stale};
    state.incidents = asArray(state.incidents).concat(
      outcomes.filter(item => item.outcome === 'NEEDS_REPAIR').map(item => ({at: Date.now(), ...item}))
    ).slice(-200);
    await Promise.all([
      setState(state),
      chrome.storage.local.set({[STORAGE_REPORT]: report})
    ]);

    if (unresolved || dedupe.duplicates || !brokerHealth.ok) {
      broker('/incident', {
        method: 'POST',
        timeout: 15000,
        body: {
          schema: 'minefield.incident/1',
          source: 'extension-v2.0.1',
          report,
          requestedActions: ['devtools-evidence', 'maintenance-chat-diagnosis']
        }
      }).catch(() => {});
    }

    try {
      chrome.action?.setBadgeText({text: unresolved ? '!' : (stale ? String(Math.min(stale, 99)) : '')});
      chrome.action?.setBadgeBackgroundColor({color: unresolved ? '#b3261e' : '#6b7280'});
    } catch (_) {}

    return report;
  }


  async function directBrowserAction(tabId, payload = {}) {
    const action = String(payload.action || '').toLowerCase();
    if (action === 'refresh' || action === 'refresh-tab' || action === 'reload-tab') {
      await chrome.tabs.reload(tabId, {bypassCache: Boolean(payload.bypassCache)});
      return {ok: true, action, tabId};
    }
    if (action === 'refresh-origin' || action === 'reload-origin') {
      const target = Number(payload.tabId || tabId);
      await chrome.tabs.reload(target, {bypassCache: Boolean(payload.bypassCache)});
      return {ok: true, action, tabId: target};
    }
    if (action === 'focus' || action === 'focus-tab') {
      const tab = await chrome.tabs.get(Number(payload.tabId || tabId));
      await chrome.windows.update(tab.windowId, {focused: true});
      await chrome.tabs.update(tab.id, {active: true});
      return {ok: true, action, tabId: tab.id, windowId: tab.windowId};
    }
    if (action === 'open' || action === 'open-url') {
      const url = String(payload.url || '');
      if (!/^https?:\/\//i.test(url)) return {ok: false, error: 'URL_DENIED'};
      const tab = await chrome.tabs.create({url, active: payload.active !== false});
      return {ok: true, action, tabId: tab.id, url: tab.url};
    }
    if (action === 'close' || action === 'close-tab') {
      const target = Number(payload.tabId || tabId);
      if (!payload.confirmed) return {ok: false, error: 'CONFIRMED_REQUIRED'};
      await chrome.tabs.remove(target);
      return {ok: true, action, tabId: target};
    }
    if (action === 'reload-extension' || action === 'restart-extension') {
      setTimeout(() => chrome.runtime.reload(), 250);
      return {ok: true, action, reloadScheduled: true};
    }
    return null;
  }

  async function executeTotalControl(message, sender) {
    const tabId = sender.tab?.id || Number(message.tabId || 0);
    const originUrl = sender.tab?.url || String(message.originUrl || '');
    const envelope = {
      id: String(message.id || `mf-${Date.now()}-${Math.random().toString(16).slice(2)}`),
      kind: String(message.kindName || message.command?.kind || message.command?.type || '').toLowerCase(),
      originUrl,
      tabId,
      payload: message.command?.payload || message.command || {}
    };
    if (!envelope.kind) return {ok: false, error: 'COMMAND_KIND_REQUIRED'};

    if (envelope.kind === 'browser') {
      const direct = await directBrowserAction(tabId, envelope.payload);
      if (direct) return {ok: Boolean(direct.ok), body: direct};
    }

    const response = await broker('/control/execute', {method: 'POST', timeout: Number(message.timeout || 120000), body: envelope});
    if (tabId) {
      chrome.tabs.sendMessage(tabId, {
        kind: 'mf.total-control.command-result',
        commandId: envelope.id,
        commandKind: envelope.kind,
        result: response,
        at: Date.now()
      }).catch(() => {});
    }
    return response;
  }

  async function getControlState() {
    const data = await chrome.storage.local.get([CONTROL_STATE_KEY]);
    return data[CONTROL_STATE_KEY] || {lastContinuationAt: 0, submissions: 0, stopped: false};
  }

  async function setControlState(state) {
    await chrome.storage.local.set({[CONTROL_STATE_KEY]: state});
  }

  async function runInternalContinuation(reason = 'alarm') {
    const status = await broker('/control/status', {timeout: 3000});
    const cfg = status?.body?.continuation;
    if (!status.ok || !cfg?.enabled || !cfg?.internalOnly) return {ok: false, outcome: 'CONTINUATION_DISABLED'};
    const until = Date.parse(cfg.until || '');
    if (Number.isFinite(until) && Date.now() >= until) return {ok: true, outcome: 'CUTOFF_REACHED'};

    const state = await getControlState();
    if (state.stopped) return {ok: true, outcome: 'STOPPED'};
    const intervalMs = Math.max(15000, Number(cfg.intervalSeconds || 60) * 1000);
    if (Date.now() - Number(state.lastContinuationAt || 0) < intervalMs) return {ok: true, outcome: 'INTERVAL_WAIT'};

    const prefix = String(cfg.conversationUrlPrefix || '');
    const tabs = await chrome.tabs.query({url: CHAT_PATTERNS}).catch(() => []);
    const candidates = tabs.filter(tab => tab.id && (!prefix || String(tab.url || '').startsWith(prefix)));
    if (!candidates.length) return {ok: false, outcome: 'CONVERSATION_TAB_NOT_FOUND'};
    const tab = candidates.find(t => t.active) || candidates[0];
    const result = await chrome.tabs.sendMessage(tab.id, {
      kind: 'mf.total-control.continue',
      prompt: String(cfg.prompt || ''),
      stopMarker: String(cfg.stopMarker || '[[MF:COMPLETE]]'),
      preserveDrafts: cfg.preserveDrafts !== false,
      reason
    }).catch(error => ({ok: false, error: String(error)}));

    state.lastContinuationAt = Date.now();
    state.lastContinuationResult = result;
    if (result?.outcome === 'STOP_MARKER_PRESENT') state.stopped = true;
    if (result?.submitted) state.submissions = Number(state.submissions || 0) + 1;
    await setControlState(state);
    return {ok: Boolean(result?.ok), tabId: tab.id, result};
  }

  chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
    if (!message || typeof message !== 'object') return undefined;

    if (message.kind === 'mf.selfrepair.heartbeat' || message.kind === 'mf.selfrepair.duplicate-content-load') {
      const tabId = sender.tab?.id;
      if (!tabId) return undefined;
      getState().then(state => {
        const key = tabKey(tabId);
        state.tabs[key] = {
          ...(state.tabs[key] || {}),
          url: sender.tab.url,
          generation: message.generation,
          actorId: message.actorId,
          lastHealthyAt: message.at || Date.now(),
          lastOutcome: message.kind === 'mf.selfrepair.heartbeat' ? 'HEALTHY' : 'DUPLICATE_SUPPRESSED',
          duplicateLoads: message.duplicateLoads || state.tabs[key]?.duplicateLoads || 0
        };
        return setState(state);
      }).catch(() => {});
      sendResponse({ok: true, version: VERSION});
      return false;
    }

    if (message.kind === 'mf.selfrepair.run') {
      investigate(message.reason || 'manual', message.suppliedContent || null).then(
        report => sendResponse({ok: true, report}),
        error => sendResponse({ok: false, error: String(error?.stack || error)})
      );
      return true;
    }

    if (message.kind === 'mf.selfrepair.status') {
      Promise.all([getState(), chrome.storage.local.get([STORAGE_REPORT, STORAGE_QUARANTINE]), broker('/health')]).then(
        ([state, storage, health]) => sendResponse({
          ok: true,
          version: VERSION,
          state,
          report: storage[STORAGE_REPORT] || null,
          quarantineCount: asArray(storage[STORAGE_QUARANTINE]).length,
          broker: health
        })
      );
      return true;
    }

    if (message.kind === 'mf.selfrepair.mcp-call') {
      broker('/mcp/call', {method: 'POST', timeout: message.timeout || 30000, body: {name: message.name, arguments: message.arguments || {}}}).then(sendResponse);
      return true;
    }

    if (message.kind === 'mf.total-control.command') {
      executeTotalControl(message, sender).then(
        result => sendResponse({ok: Boolean(result?.ok), result}),
        error => sendResponse({ok: false, error: String(error?.stack || error)})
      );
      return true;
    }

    if (message.kind === 'mf.total-control.continue-now') {
      runInternalContinuation(message.reason || 'manual').then(sendResponse);
      return true;
    }

    if (message.kind === 'mf.total-control.status') {
      Promise.all([broker('/control/status'), getControlState()]).then(([control, state]) => sendResponse({ok: true, version: VERSION, control, state}));
      return true;
    }

    return undefined;
  });

  chrome.alarms?.onAlarm.addListener(alarm => {
    if (alarm.name === ALARM) investigate('alarm').catch(() => {});
    if (alarm.name === CONTINUATION_ALARM) runInternalContinuation('alarm').catch(() => {});
  });

  chrome.runtime.onStartup?.addListener(() => investigate('startup').catch(() => {}));
  chrome.runtime.onInstalled?.addListener(() => investigate('installed').catch(() => {}));

  try {
    chrome.alarms?.create(ALARM, {delayInMinutes: 0.25, periodInMinutes: 1});
    chrome.alarms?.create(CONTINUATION_ALARM, {delayInMinutes: 0.2, periodInMinutes: 1});
  } catch (_) {}

  globalThis.mfSelfRepairV200 = {investigate, broker, version: VERSION};
  globalThis.mfTotalControlV200 = {executeTotalControl, runInternalContinuation, broker, version: VERSION};
  try {
    globalThis.mfRunWatchdogInvestigator = investigate;
  } catch (_) {}

  setTimeout(() => investigate('module-boot').catch(() => {}), 1500);
  setTimeout(() => runInternalContinuation('module-boot').catch(() => {}), 5000);
})();

;(() => {
  'use strict';

  const VERSION = '2.0.1';
  const API = 'http://127.0.0.1:9789';
  const BROKER_TOKEN = '__MF_BROKER_TOKEN__';
  const GLOBAL = Symbol.for('doubletab.minefield.artifact-mesh.background.v200');
  const STORE = {
    links: 'mfArtifactMeshZipLinksV200',
    downloads: 'mfArtifactMeshDownloadsV200',
    actors: 'mfArtifactMeshChatActorsV200',
    delivered: 'mfArtifactMeshDeliveredReturnsV200'
  };
  const RETURN_ALARM = 'mf-artifact-return-pump-v200';
  const LOCATION_ALARM = 'mf-chat-location-heartbeat-v200';
  if (globalThis[GLOBAL]) return;
  globalThis[GLOBAL] = {version: VERSION, startedAt: Date.now()};

  /* @mf-contract brokerRequest
     ingress: path:string beginning '/', options:plain-object|undefined
     compute: authenticated loopback fetch with bounded timeout and JSON-or-raw decoding
     egress: {ok:boolean,status?:number,body?:object,error?:string}; never throws
     test-mode: replace fetch/AbortController with generated state machine
     production-mode: retain ingress and egress guards; internal redundant assertions may compile away */
  async function brokerRequest(path, options = {}) {
    if (typeof path !== 'string' || !path.startsWith('/')) return {ok: false, error: 'INVALID_BROKER_PATH'};
    if (!options || typeof options !== 'object' || Array.isArray(options)) options = {};
    const controller = new AbortController();
    const timeoutMs = Number.isFinite(Number(options.timeout)) ? Math.max(250, Math.min(120000, Number(options.timeout))) : 7000;
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const response = await fetch(`${API}${path}`, {
        method: typeof options.method === 'string' ? options.method : 'GET',
        headers: {'content-type': 'application/json', 'x-minefield-token': BROKER_TOKEN},
        body: options.body === undefined ? undefined : JSON.stringify(options.body),
        signal: controller.signal
      });
      const text = await response.text();
      let body;
      try { body = JSON.parse(text); } catch (_) { body = {raw: text}; }
      const result = {ok: Boolean(response.ok), status: Number(response.status), body};
      return result && typeof result.ok === 'boolean' ? result : {ok: false, error: 'BROKER_RESULT_INVALID'};
    } catch (error) {
      return {ok: false, error: String(error?.message || error || 'BROKER_FAILURE')};
    } finally {
      clearTimeout(timer);
    }
  }

  const Contract = globalThis.MFContractCoreV200;
  if (!Contract || Contract.version !== VERSION) throw new Error('MF_CONTRACT_CORE_V200_MISSING');
  const safeArray = Contract.safeArray;
  const safeObject = Contract.safeObject;
  const canonicalChatIdentity = Contract.canonicalChatIdentity;
  const classifyZipLink = Contract.classifyZipLink;

  async function getStore(key, fallback) {
    const data = await chrome.storage.local.get([key]);
    const value = data[key];
    return value === undefined ? fallback : value;
  }

  async function setStore(key, value) { await chrome.storage.local.set({[key]: value}); }

  async function registerLocation(tab, state = 'BOUND') {
    if (!tab || !Number.isInteger(tab.id) || typeof tab.url !== 'string') return {ok: false, error: 'INVALID_TAB'};
    const identity = canonicalChatIdentity(tab.url);
    if (!identity) return {ok: false, error: 'NOT_CHAT_CONVERSATION'};
    const actors = safeObject(await getStore(STORE.actors, {}));
    const prior = safeObject(actors[identity.key]);
    const actorId = typeof prior.tabActorId === 'string' && prior.tabActorId ? prior.tabActorId : crypto.randomUUID();
    const generation = `${tab.id}:${tab.windowId}:${Date.now()}`;
    const loc = {
      schema: 'minefield.chat-location/1', chatKey: identity.key, canonicalUrl: identity.canonicalUrl,
      provider: identity.provider, projectId: identity.projectId, conversationId: identity.conversationId,
      tabActorId: actorId, tabId: tab.id, windowId: tab.windowId, generation, state,
      registeredAt: new Date().toISOString()
    };
    actors[identity.key] = loc;
    await setStore(STORE.actors, actors);
    const response = await brokerRequest('/locator/register', {method: 'POST', body: loc});
    return {ok: Boolean(response.ok), identity, location: loc, broker: response};
  }

  async function focusExistingChat(tabId, rawUrl) {
    if (!Number.isInteger(tabId)) return {ok: false, error: 'INVALID_TAB_ID'};
    const identity = canonicalChatIdentity(rawUrl);
    if (!identity) return {ok: false, outcome: 'NOT_CHAT_CONVERSATION'};
    const actors = safeObject(await getStore(STORE.actors, {}));
    const prior = safeObject(actors[identity.key]);
    if (Number.isInteger(prior.tabId) && prior.tabId !== tabId) {
      try {
        const existing = await chrome.tabs.get(prior.tabId);
        if (existing && existing.url && canonicalChatIdentity(existing.url)?.key === identity.key) {
          await chrome.windows.update(existing.windowId, {focused: true});
          await chrome.tabs.update(existing.id, {active: true});
          await chrome.tabs.remove(tabId);
          return {ok: true, outcome: 'SWITCHED_TO_EXISTING', existingTabId: existing.id, closedDuplicateTabId: tabId};
        }
      } catch (_) {}
    }
    return registerLocation(await chrome.tabs.get(tabId));
  }

  /* @mf-contract startZipDownload
     ingress: untrusted page link message plus Chrome sender metadata
     compute: classify ZIP, bind canonical origin actor, persist broker registration, request one authenticated browser download
     egress: bounded result object; failure remains retryable and never mutates filesystem directly
     test-mode: substitute storage/download/broker state machines including interruption and duplicate naming
     extension-mode: bind real chrome.downloads and authenticated loopback broker
     production-mode: retain all page, identity, broker and API boundary guards */
  async function startZipDownload(message, sender) {
    const mode = message?.mode === 'dom-click' ? 'dom-click' : 'download';
    const expectedHint = message?.expectedName || message?.downloadName || '';
    let link = classifyZipLink(message?.href, expectedHint);
    if (!link && mode === 'dom-click' && typeof message?.href === 'string' && /^(sandbox:|blob:)/i.test(message.href) && typeof expectedHint === 'string' && /^[^\/\\\0]{1,240}\.zip$/i.test(expectedHint)) {
      link = {url: message.href, expectedName: expectedHint};
    }
    if (!link) return {ok: false, error: 'INVALID_ZIP_LINK'};
    const status = await brokerRequest('/control/status', {timeout: 3000});
    if (status.ok && status.body?.artifacts?.autoDownloadZipLinks === false) return {ok: false, error: 'AUTO_ZIP_DOWNLOAD_DISABLED'};
    const originUrl = typeof message.originUrl === 'string' ? message.originUrl : sender?.tab?.url || '';
    const identity = canonicalChatIdentity(originUrl);
    if (!identity || !sender?.tab?.id) return {ok: false, error: 'ORIGIN_CHAT_REQUIRED'};
    const fingerprint = await sha256(`${identity.key}\n${link.url}\n${link.expectedName}`);
    const links = safeObject(await getStore(STORE.links, {}));
    if (links[fingerprint]?.state === 'REQUESTED' || links[fingerprint]?.state === 'COMPLETE') {
      return {ok: true, outcome: 'DUPLICATE_LINK_SUPPRESSED', fingerprint};
    }
    links[fingerprint] = {state: 'REQUESTING', mode, url: link.url, expectedName: link.expectedName, originUrl, chatKey: identity.key, at: Date.now()};
    await setStore(STORE.links, links);
    const actorLoc = safeObject((safeObject(await getStore(STORE.actors, {})))[identity.key]);
    const registration = await brokerRequest('/artifact/register', {method: 'POST', body: {
      schema: 'minefield.artifact-intake/1', expectedName: link.expectedName, sourceUrl: link.url,
      originUrl, conversationKey: identity.key, tabActorId: actorLoc.tabActorId || '', tabId: sender.tab.id
    }});
    if (!registration.ok) {
      links[fingerprint] = {...links[fingerprint], state: 'FAILED_REGISTER', registration, at: Date.now()};
      await setStore(STORE.links, links);
      return {ok: false, error: 'ARTIFACT_ORIGIN_REGISTRATION_FAILED', registration};
    }
    if (mode === 'dom-click') {
      links[fingerprint] = {...links[fingerprint], state: 'REQUESTED', mode, at: Date.now()};
      await setStore(STORE.links, links);
      return {ok: true, outcome: 'DOM_CLICK_REQUIRED', fingerprint};
    }
    let downloadId;
    try {
      downloadId = await chrome.downloads.download({url: link.url, saveAs: false, conflictAction: 'uniquify'});
    } catch (error) {
      links[fingerprint] = {...links[fingerprint], state: 'FAILED', error: String(error?.message || error), at: Date.now()};
      await setStore(STORE.links, links);
      return {ok: false, error: String(error?.message || error)};
    }
    const downloads = safeObject(await getStore(STORE.downloads, {}));
    downloads[String(downloadId)] = {fingerprint, expectedName: link.expectedName, sourceUrl: link.url, originUrl, conversationKey: identity.key, tabActorId: actorLoc.tabActorId || '', tabId: sender.tab.id, state: 'IN_PROGRESS'};
    links[fingerprint] = {...links[fingerprint], state: 'REQUESTED', downloadId, at: Date.now()};
    await chrome.storage.local.set({[STORE.links]: links, [STORE.downloads]: downloads});
    return {ok: true, outcome: 'DOWNLOAD_STARTED', downloadId, fingerprint};
  }

  async function updateDownloadRegistration(downloadId) {
    if (!Number.isInteger(downloadId)) return;
    const downloads = safeObject(await getStore(STORE.downloads, {}));
    const record = safeObject(downloads[String(downloadId)]);
    if (!record.originUrl) return;
    const items = await chrome.downloads.search({id: downloadId});
    const item = items?.[0];
    if (!item) return;
    const body = {
      schema: 'minefield.artifact-intake/1', downloadId, expectedName: record.expectedName,
      finalPath: item.filename || '', sourceUrl: record.sourceUrl, originUrl: record.originUrl,
      conversationKey: record.conversationKey, tabActorId: record.tabActorId, tabId: record.tabId
    };
    await brokerRequest('/artifact/register', {method: 'POST', body});
    downloads[String(downloadId)] = {...record, finalPath: item.filename || '', state: item.state || record.state, updatedAt: Date.now()};
    await setStore(STORE.downloads, downloads);
  }

  function formatReturnEnvelope(ret) {
    const safe = safeObject(ret);
    return `[[MINEFIELD_RETURN/1]]\n${JSON.stringify(safe)}\n[[/MINEFIELD_RETURN]]`;
  }

  async function findReturnTab(ret) {
    if (Number.isInteger(ret.tabId)) {
      try { const tab = await chrome.tabs.get(ret.tabId); if (tab) return tab; } catch (_) {}
    }
    const actors = safeObject(await getStore(STORE.actors, {}));
    if (ret.conversationKey && actors[ret.conversationKey]?.tabId) {
      try { const tab = await chrome.tabs.get(actors[ret.conversationKey].tabId); if (tab) return tab; } catch (_) {}
    }
    if (typeof ret.originUrl === 'string') {
      const identity = canonicalChatIdentity(ret.originUrl);
      if (identity) {
        const tabs = await chrome.tabs.query({url: ['https://chatgpt.com/*', 'https://chat.openai.com/*']});
        const found = tabs.find(tab => canonicalChatIdentity(tab.url || '')?.key === identity.key);
        if (found) return found;
      }
    }
    return null;
  }

  /* @mf-contract pumpReturns
     ingress: authenticated broker pending-return array of unknown values
     compute: validate READY envelope, resolve durable actor, verify exact-origin submission postcondition, then ack
     egress: bounded counts; no current-focus fallback and no ack before observed delivery
     test-mode: enumerate missing tabs, stale bindings, drafts, reload crashes and duplicate delivery windows
     extension-mode: bind Chrome tab/storage/message APIs
     production-mode: retain identity, state, draft, postcondition and idempotency guards */
  async function pumpReturns() {
    const response = await brokerRequest('/return/pending', {timeout: 8000});
    const returns = safeArray(response?.body?.returns);
    if (!response.ok || !returns.length) return {ok: Boolean(response.ok), count: returns.length};
    const delivered = safeObject(await getStore(STORE.delivered, {}));
    let deliveredCount = 0;
    for (const rawReturn of returns) {
      const normalized = Contract.validateReturnEnvelope(rawReturn);
      if (!normalized) {
        await brokerRequest('/incident', {method: 'POST', body: {
          schema: 'minefield.incident/1', source: 'return-pump-v2.0.1',
          originUrl: typeof rawReturn?.originUrl === 'string' ? rawReturn.originUrl : '',
          error: 'INVALID_RETURN_ENVELOPE', returnPreview: JSON.stringify(rawReturn).slice(0, 12000)
        }});
        continue;
      }
      const ret = {...rawReturn, ...normalized};
      if (delivered[ret.id]) {
        await brokerRequest('/return/ack', {method: 'POST', body: {id: ret.id, outcome: 'DUPLICATE_ALREADY_DELIVERED', evidence: delivered[ret.id]}});
        continue;
      }
      const tab = await findReturnTab(ret);
      if (!tab?.id) continue;
      let result;
      try {
        result = await chrome.tabs.sendMessage(tab.id, {kind: 'mf.return.deliver', returnEnvelope: ret, text: formatReturnEnvelope(ret)});
      } catch (error) {
        result = {ok: false, error: String(error?.message || error)};
      }
      if (result?.ok && (result.submitted || result.duplicate)) {
        const evidence = {tabId: tab.id, windowId: tab.windowId, result, at: new Date().toISOString()};
        delivered[ret.id] = evidence;
        deliveredCount += 1;
        await setStore(STORE.delivered, delivered);
        await brokerRequest('/return/ack', {method: 'POST', body: {id: ret.id, outcome: result.duplicate ? 'DUPLICATE_SUPPRESSED' : 'SUBMITTED_EXACT_ORIGIN', evidence}});
      }
    }
    return {ok: true, count: returns.length, delivered: deliveredCount};
  }


  async function forwardRuntimeIncident(message, sender) {
    const originUrl = typeof message?.originUrl === 'string' ? message.originUrl : sender?.tab?.url || '';
    const identity = canonicalChatIdentity(originUrl);
    const actors = safeObject(await getStore(STORE.actors, {}));
    const actor = identity ? safeObject(actors[identity.key]) : {};
    return brokerRequest('/incident', {method: 'POST', timeout: 15000, body: {
      schema: 'minefield.incident/1', source: String(message?.source || 'extension-runtime-v2.0.1').slice(0, 256),
      originUrl, conversationKey: identity?.key || '', tabId: sender?.tab?.id || 0,
      tabActorId: actor.tabActorId || '', details: safeObject(message?.details),
      receivedAt: new Date().toISOString(), requestedActions: ['replay', 'devtools-evidence', 'maintenance-chat-diagnosis']
    }});
  }

  chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
    if (!message || typeof message !== 'object') return undefined;
    if (message.kind === 'mf.artifact.zip-link') {
      startZipDownload(message, sender).then(sendResponse, error => sendResponse({ok: false, error: String(error?.stack || error)}));
      return true;
    }
    if (message.kind === 'mf.artifact.return-pump') {
      pumpReturns().then(sendResponse);
      return true;
    }
    if (message.kind === 'mf.locator.register-current') {
      const tab = sender.tab;
      registerLocation(tab).then(sendResponse);
      return true;
    }
    if (message.kind === 'mf.runtime.incident') {
      forwardRuntimeIncident(message, sender).then(sendResponse, error => sendResponse({ok: false, error: String(error?.stack || error)}));
      return true;
    }
    return undefined;
  });

  chrome.downloads?.onChanged.addListener(delta => {
    if (!Number.isInteger(delta?.id)) return;
    if (delta.state?.current === 'complete' || delta.filename?.current || delta.error?.current) {
      updateDownloadRegistration(delta.id).catch(() => {});
    }
  });

  chrome.tabs?.onCreated.addListener(tab => {
    if (tab?.id && tab.pendingUrl) focusExistingChat(tab.id, tab.pendingUrl).catch(() => {});
  });
  chrome.tabs?.onUpdated.addListener((tabId, change, tab) => {
    const url = change.url || tab?.url;
    if (url) focusExistingChat(tabId, url).catch(() => {});
  });
  chrome.tabs?.onRemoved.addListener(async tabId => {
    const actors = safeObject(await getStore(STORE.actors, {}));
    let changed = false;
    for (const [key, loc] of Object.entries(actors)) {
      if (loc?.tabId === tabId) { actors[key] = {...loc, state: 'UNBOUND', tabId: null, registeredAt: new Date().toISOString()}; changed = true; }
    }
    if (changed) await setStore(STORE.actors, actors);
  });

  chrome.alarms?.onAlarm.addListener(alarm => {
    if (alarm.name === RETURN_ALARM) pumpReturns().catch(() => {});
    if (alarm.name === LOCATION_ALARM) chrome.tabs.query({url: ['https://chatgpt.com/*', 'https://chat.openai.com/*']}).then(tabs => Promise.all(tabs.map(tab => registerLocation(tab))).catch(() => {})).catch(() => {});
  });
  try {
    chrome.alarms?.create(RETURN_ALARM, {delayInMinutes: 0.1, periodInMinutes: 1});
    chrome.alarms?.create(LOCATION_ALARM, {delayInMinutes: 0.2, periodInMinutes: 1});
  } catch (_) {}

  chrome.runtime.onStartup?.addListener(() => {
    pumpReturns().catch(() => {});
    chrome.tabs.query({url: ['https://chatgpt.com/*', 'https://chat.openai.com/*']}).then(tabs => Promise.all(tabs.map(tab => registerLocation(tab))).catch(() => {})).catch(() => {});
  });

  globalThis.mfArtifactMeshV200 = {brokerRequest, canonicalChatIdentity, classifyZipLink, registerLocation, pumpReturns, version: VERSION};
  setTimeout(() => pumpReturns().catch(() => {}), 3000);
})();
