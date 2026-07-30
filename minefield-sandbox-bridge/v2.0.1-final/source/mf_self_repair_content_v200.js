(() => {
  'use strict';

  const VERSION = '2.0.1';
  const GLOBAL_KEY = Symbol.for('doubletab.minefield.selfrepair.content.v200');
  const generation = `${Math.trunc(performance.timeOrigin || Date.now())}:${location.href}`;
  const existing = globalThis[GLOBAL_KEY];

  if (existing && existing.generation === generation) {
    existing.duplicateLoads = (existing.duplicateLoads || 0) + 1;
    try {
      chrome.runtime.sendMessage({
        kind: 'mf.selfrepair.duplicate-content-load',
        version: VERSION,
        generation,
        actorId: existing.actorId,
        duplicateLoads: existing.duplicateLoads,
        url: location.href,
        at: Date.now()
      }).catch(() => {});
    } catch (_) {}
    return;
  }

  const actorId = (crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(16).slice(2)}`);
  const state = {
    version: VERSION,
    generation,
    actorId,
    startedAt: Date.now(),
    lastHeartbeatAt: 0,
    duplicateLoads: 0,
    observerEvents: 0,
    ownedMutationsIgnored: 0
  };
  globalThis[GLOBAL_KEY] = state;

  function markOwned(node) {
    if (node && node.nodeType === Node.ELEMENT_NODE) {
      node.setAttribute('data-minefield-owned', '1');
      node.setAttribute('data-mf-owner-version', VERSION);
    }
    return node;
  }

  function isOwned(node) {
    if (!node) return false;
    const element = node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement;
    return Boolean(element && element.closest && element.closest('[data-minefield-owned="1"]'));
  }

  globalThis.__mfMarkOwnedNode = markOwned;
  globalThis.__mfIsOwnedNode = isOwned;

  function findComposer() {
    const selectors = [
      '#prompt-textarea',
      '[data-testid="composer-text-input"]',
      '[data-testid*="composer"] [contenteditable="true"]',
      'form [contenteditable="true"][role="textbox"]',
      'textarea[placeholder]'
    ];
    for (const selector of selectors) {
      const node = document.querySelector(selector);
      if (node && node.getClientRects().length) return node;
    }
    return null;
  }

  function readComposer(node) {
    if (!node) return '';
    if ('value' in node) return String(node.value || '');
    return String(node.innerText || node.textContent || '');
  }

  function setComposer(node, text) {
    if (!node) throw new Error('COMPOSER_NOT_FOUND');
    node.focus();
    if ('value' in node) {
      const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(node), 'value')?.set;
      if (setter) setter.call(node, text);
      else node.value = text;
      node.dispatchEvent(new Event('input', {bubbles: true, composed: true}));
      node.dispatchEvent(new Event('change', {bubbles: true, composed: true}));
      return;
    }
    node.textContent = text;
    node.dispatchEvent(new InputEvent('input', {
      bubbles: true,
      composed: true,
      inputType: 'insertText',
      data: text
    }));
  }

  function findSendButton() {
    const selectors = [
      '[data-testid="send-button"]',
      'button[aria-label*="Send" i]',
      'button[aria-label*="Verstuur" i]',
      'form button[type="submit"]'
    ];
    for (const selector of selectors) {
      const node = document.querySelector(selector);
      if (node && !node.disabled && node.getClientRects().length) return node;
    }
    return null;
  }

  function assistantSnapshot() {
    const nodes = Array.from(document.querySelectorAll('[data-message-author-role="assistant"], article'));
    const last = nodes[nodes.length - 1];
    const text = String(last?.innerText || last?.textContent || '').trim();
    return {
      count: nodes.length,
      lastText: text.slice(-12000),
      hasRepairEnvelope: text.includes('[[MINEFIELD_REPAIR/1]]')
    };
  }

  async function submitMaintenance(prompt) {
    const composer = findComposer();
    if (!composer) return {ok: false, error: 'COMPOSER_NOT_FOUND'};
    const draft = readComposer(composer);
    if (draft.trim()) {
      return {ok: false, error: 'HUMAN_DRAFT_PRESENT', draftLength: draft.length};
    }
    setComposer(composer, prompt);
    await new Promise(resolve => setTimeout(resolve, 120));
    const send = findSendButton();
    if (send) {
      send.click();
      return {ok: true, method: 'button'};
    }
    composer.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', code: 'Enter', bubbles: true, cancelable: true}));
    composer.dispatchEvent(new KeyboardEvent('keyup', {key: 'Enter', code: 'Enter', bubbles: true, cancelable: true}));
    return {ok: true, method: 'enter'};
  }

  async function heartbeat(reason = 'interval') {
    state.lastHeartbeatAt = Date.now();
    const composer = findComposer();
    const payload = {
      kind: 'mf.selfrepair.heartbeat',
      version: VERSION,
      actorId,
      generation,
      reason,
      at: state.lastHeartbeatAt,
      href: location.href,
      title: document.title,
      visibility: document.visibilityState,
      readyState: document.readyState,
      composerPresent: Boolean(composer),
      composerDraftLength: readComposer(composer).length,
      observerEvents: state.observerEvents,
      ownedMutationsIgnored: state.ownedMutationsIgnored
    };
    try {
      await chrome.runtime.sendMessage(payload);
    } catch (_) {}
    return payload;
  }

  const observer = new MutationObserver(records => {
    for (const record of records) {
      if (isOwned(record.target)) {
        state.ownedMutationsIgnored += 1;
        continue;
      }
      let allOwned = true;
      for (const node of record.addedNodes || []) {
        if (!isOwned(node)) {
          allOwned = false;
          break;
        }
      }
      if (allOwned && record.addedNodes?.length) {
        state.ownedMutationsIgnored += 1;
        continue;
      }
      state.observerEvents += 1;
    }
  });

  observer.observe(document.documentElement || document, {subtree: true, childList: true});

  chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
    if (!message || typeof message !== 'object') return undefined;
    if (message.kind === 'mf.selfrepair.probe') {
      heartbeat('probe').then(data => sendResponse({ok: true, ...data}));
      return true;
    }
    if (message.kind === 'mf.selfrepair.snapshot') {
      sendResponse({
        ok: true,
        version: VERSION,
        actorId,
        generation,
        url: location.href,
        title: document.title,
        visibility: document.visibilityState,
        readyState: document.readyState,
        composerPresent: Boolean(findComposer()),
        assistant: assistantSnapshot(),
        state: {...state}
      });
      return false;
    }
    if (message.kind === 'mf.selfrepair.submit-maintenance') {
      submitMaintenance(String(message.prompt || '')).then(sendResponse);
      return true;
    }
    return undefined;
  });

  window.addEventListener('pageshow', () => heartbeat('pageshow'), {passive: true});
  document.addEventListener('visibilitychange', () => heartbeat('visibilitychange'), {passive: true});
  setInterval(() => heartbeat('interval'), 5000);
  heartbeat('boot');
})();

;(() => {
  'use strict';

  const VERSION = '2.0.1';
  const GLOBAL = Symbol.for('doubletab.minefield.total-control.content.v200');
  if (globalThis[GLOBAL]) return;

  const state = globalThis[GLOBAL] = {
    version: VERSION,
    startedAt: Date.now(),
    seen: new Set(),
    lastScanAt: 0,
    commandCount: 0,
    resultCount: 0,
    continuationSubmissions: 0
  };

  const COMMAND_TYPES = new Set(['POWERSHELL', 'CMD', 'EXEC', 'FS', 'FILESYSTEM', 'WATCH', 'BROWSER', 'CUA', 'CONTINUE', 'CONTINUATION', 'RECORD', 'NOTE', 'ARTIFACT', 'LOCATOR']);

  function hashText(text) {
    let h = 2166136261;
    for (let i = 0; i < text.length; i += 1) {
      h ^= text.charCodeAt(i);
      h = Math.imul(h, 16777619);
    }
    return (h >>> 0).toString(16).padStart(8, '0');
  }

  function assistantNodes() {
    const primary = Array.from(document.querySelectorAll('[data-message-author-role="assistant"]'));
    if (primary.length) return primary;
    return Array.from(document.querySelectorAll('article')).filter(node => {
      const text = String(node.innerText || node.textContent || '');
      return text.includes('[[MF:') || text.includes('[[MINEFIELD_');
    });
  }

  function readNode(node) {
    return String(node?.innerText || node?.textContent || '');
  }

  function parseJson(text) {
    try { return JSON.parse(text); } catch (_) { return null; }
  }

  function normalizeCommand(type, body, raw) {
    let kind = String(type || '').toLowerCase();
    if (kind === 'continue') kind = 'continuation';
    if (kind === 'filesystem') kind = 'fs';
    let payload = body;
    if (payload === null || typeof payload !== 'object' || Array.isArray(payload)) {
      if (kind === 'powershell') payload = {script: String(body ?? '')};
      else if (kind === 'cmd') payload = {command: String(body ?? '')};
      else payload = {value: body};
    }
    return {
      id: `mf-${Date.now()}-${hashText(raw).slice(0, 8)}`,
      kind,
      payload,
      raw
    };
  }

  function extractCommands(text) {
    const commands = [];

    const canonical = /\[\[MF:COMMAND\/1\]\]([\s\S]*?)\[\[\/MF:COMMAND\]\]/g;
    for (const match of text.matchAll(canonical)) {
      const body = parseJson(match[1].trim());
      if (body && typeof body === 'object') {
        commands.push(normalizeCommand(body.kind || body.type, body.payload || body, match[0]));
      }
    }

    const compactJson = /\[\[MF:(\{[\s\S]*?\})\]\]/g;
    for (const match of text.matchAll(compactJson)) {
      const body = parseJson(match[1]);
      if (body && typeof body === 'object') {
        commands.push(normalizeCommand(body.kind || body.type, body.payload || body, match[0]));
      }
    }

    const typed = /\[\[MF:([A-Z_]+)(?:\/1)?\s+([\s\S]*?)\]\]/g;
    for (const match of text.matchAll(typed)) {
      const type = match[1].toUpperCase();
      if (!COMMAND_TYPES.has(type)) continue;
      const bodyText = match[2].trim();
      const body = parseJson(bodyText) ?? bodyText;
      commands.push(normalizeCommand(type, body, match[0]));
    }

    const blockTyped = /\[\[MF:([A-Z_]+)\/1\]\]([\s\S]*?)\[\[\/MF:\1\]\]/g;
    for (const match of text.matchAll(blockTyped)) {
      const type = match[1].toUpperCase();
      if (!COMMAND_TYPES.has(type)) continue;
      const bodyText = match[2].trim();
      const body = parseJson(bodyText) ?? bodyText;
      commands.push(normalizeCommand(type, body, match[0]));
    }

    const unique = [];
    const seenRaw = new Set();
    for (const command of commands) {
      const key = hashText(command.raw);
      if (seenRaw.has(key)) continue;
      seenRaw.add(key);
      unique.push(command);
    }
    return unique;
  }

  async function dispatch(command) {
    const fingerprint = `${location.href}|${hashText(command.raw)}`;
    if (state.seen.has(fingerprint)) return {ok: true, outcome: 'DUPLICATE_SUPPRESSED'};
    state.seen.add(fingerprint);
    if (state.seen.size > 2000) state.seen = new Set(Array.from(state.seen).slice(-1000));
    state.commandCount += 1;
    try {
      return await chrome.runtime.sendMessage({
        kind: 'mf.total-control.command',
        id: command.id,
        kindName: command.kind,
        command: {kind: command.kind, payload: command.payload},
        originUrl: location.href,
        timeout: Number(command.payload?.timeoutMs || 120000)
      });
    } catch (error) {
      return {ok: false, error: String(error?.stack || error)};
    }
  }

  async function scan(reason = 'mutation') {
    state.lastScanAt = Date.now();
    const nodes = assistantNodes();
    for (const node of nodes.slice(-40)) {
      const text = readNode(node);
      if (!text.includes('[[MF:')) continue;
      for (const command of extractCommands(text)) {
        await dispatch(command);
      }
    }
    return {ok: true, reason, nodes: nodes.length, commandCount: state.commandCount};
  }

  function findComposer() {
    const selectors = [
      '#prompt-textarea',
      '[data-testid="composer-text-input"]',
      '[data-testid*="composer"] [contenteditable="true"]',
      'form [contenteditable="true"][role="textbox"]',
      'textarea[placeholder]'
    ];
    for (const selector of selectors) {
      const node = document.querySelector(selector);
      if (node && node.getClientRects().length) return node;
    }
    return null;
  }

  function composerText(node) {
    if (!node) return '';
    return 'value' in node ? String(node.value || '') : String(node.innerText || node.textContent || '');
  }

  function setComposer(node, text) {
    if (!node) throw new Error('COMPOSER_NOT_FOUND');
    node.focus();
    if ('value' in node) {
      const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(node), 'value')?.set;
      if (setter) setter.call(node, text);
      else node.value = text;
      node.dispatchEvent(new Event('input', {bubbles: true, composed: true}));
      node.dispatchEvent(new Event('change', {bubbles: true, composed: true}));
      return;
    }
    node.textContent = text;
    node.dispatchEvent(new InputEvent('input', {bubbles: true, composed: true, inputType: 'insertText', data: text}));
  }

  function isGenerating() {
    return Boolean(document.querySelector('[data-testid="stop-button"], button[aria-label*="Stop" i], button[aria-label*="Stop generating" i]'));
  }

  function stopMarkerPresent(marker) {
    const nodes = assistantNodes();
    const recent = nodes.slice(-8).map(readNode).join('\n');
    return marker && recent.includes(marker);
  }

  function findSendButton() {
    return [
      '[data-testid="send-button"]',
      'button[aria-label*="Send" i]',
      'button[aria-label*="Verstuur" i]',
      'form button[type="submit"]'
    ].map(selector => document.querySelector(selector)).find(node => node && !node.disabled && node.getClientRects().length) || null;
  }

  async function submitText(text, options = {}) {
    const composer = findComposer();
    if (!composer) return {ok: false, outcome: 'COMPOSER_NOT_FOUND'};
    const draft = composerText(composer);
    if (options.preserveDrafts !== false && draft.trim()) return {ok: true, outcome: 'HUMAN_DRAFT_PRESENT', draftLength: draft.length};
    if (isGenerating()) return {ok: true, outcome: 'ASSISTANT_GENERATING'};
    setComposer(composer, text);
    await new Promise(resolve => setTimeout(resolve, 180));
    const button = findSendButton();
    if (button) {
      button.click();
      return {ok: true, submitted: true, method: 'button'};
    }
    composer.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', code: 'Enter', bubbles: true, cancelable: true}));
    composer.dispatchEvent(new KeyboardEvent('keyup', {key: 'Enter', code: 'Enter', bubbles: true, cancelable: true}));
    return {ok: true, submitted: true, method: 'enter'};
  }

  async function handleContinuation(message) {
    const marker = String(message.stopMarker || '[[MF:COMPLETE]]');
    if (stopMarkerPresent(marker)) return {ok: true, outcome: 'STOP_MARKER_PRESENT'};
    const result = await submitText(String(message.prompt || ''), {preserveDrafts: message.preserveDrafts !== false});
    if (result.submitted) state.continuationSubmissions += 1;
    return result;
  }

  async function handleCommandResult(message) {
    state.resultCount += 1;
    const result = message.result || {};
    let payload = '';
    try { payload = JSON.stringify(result); } catch (_) { payload = String(result); }
    if (payload.length > 24000) payload = `${payload.slice(0, 24000)}...[TRUNCATED]`;
    const text = `MINEFIELD CONTROL RESULT [${message.commandId || 'unknown'}]\n[[MINEFIELD_CONTROL_RESULT/1]]\n${payload}\n[[/MINEFIELD_CONTROL_RESULT]]`;
    return submitText(text, {preserveDrafts: true});
  }

  chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
    if (!message || typeof message !== 'object') return undefined;
    if (message.kind === 'mf.total-control.continue') {
      handleContinuation(message).then(sendResponse);
      return true;
    }
    if (message.kind === 'mf.total-control.command-result') {
      handleCommandResult(message).then(sendResponse);
      return true;
    }
    if (message.kind === 'mf.total-control.scan') {
      scan(message.reason || 'manual').then(sendResponse);
      return true;
    }
    return undefined;
  });

  window.addEventListener('error', event => {
    reportRuntimeIncident('window.error', {message: event.message, stack: event.error?.stack, source: event.filename, line: event.lineno, column: event.colno});
  }, {capture: true});
  window.addEventListener('unhandledrejection', event => {
    const reason = event.reason;
    reportRuntimeIncident('unhandledrejection', {message: reason?.message || String(reason), stack: reason?.stack || ''});
  });

  const observer = new MutationObserver(() => {
    clearTimeout(state.scanTimer);
    state.scanTimer = setTimeout(() => scan('mutation').catch(() => {}), 400);
  });
  observer.observe(document.documentElement || document, {subtree: true, childList: true, characterData: true});
  setInterval(() => scan('interval').catch(() => {}), 5000);
  setTimeout(() => scan('boot').catch(() => {}), 1200);
})();

;(() => {
  'use strict';

  const VERSION = '2.0.1';
  const GLOBAL = Symbol.for('doubletab.minefield.artifact-mesh.content.v200');
  const Contract = globalThis.MFContractCoreV200;
  if (!Contract || Contract.version !== VERSION) throw new Error('MF_CONTRACT_CORE_V200_MISSING');
  if (globalThis[GLOBAL]) return;
  const state = globalThis[GLOBAL] = {
    version: VERSION, startedAt: Date.now(), seenLinks: new Set(), deliveredReturns: new Set(), scans: 0
  };

  function findComposer() {
    const selectors = [
      '#prompt-textarea',
      '[data-testid="composer-text-input"]',
      '[data-testid*="composer"] [contenteditable="true"]',
      'form [contenteditable="true"][role="textbox"]',
      'textarea[placeholder]'
    ];
    for (const selector of selectors) {
      const node = document.querySelector(selector);
      if (node && node.getClientRects().length) return node;
    }
    return null;
  }

  function composerText(node) {
    if (!node) return '';
    return 'value' in node ? String(node.value || '') : String(node.innerText || node.textContent || '');
  }

  function setComposer(node, text) {
    if (!node || typeof text !== 'string') throw new Error('INVALID_COMPOSER_WRITE');
    node.focus();
    if ('value' in node) {
      const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(node), 'value')?.set;
      if (setter) setter.call(node, text); else node.value = text;
      node.dispatchEvent(new Event('input', {bubbles: true, composed: true}));
      node.dispatchEvent(new Event('change', {bubbles: true, composed: true}));
      return;
    }
    node.textContent = text;
    node.dispatchEvent(new InputEvent('input', {bubbles: true, composed: true, inputType: 'insertText', data: text}));
  }

  function findSendButton() {
    return [
      '[data-testid="send-button"]',
      'button[aria-label*="Send" i]',
      'button[aria-label*="Verstuur" i]',
      'form button[type="submit"]'
    ].map(selector => document.querySelector(selector)).find(node => node && !node.disabled && node.getClientRects().length) || null;
  }

  function isGenerating() {
    return Boolean(document.querySelector('[data-testid="stop-button"],button[aria-label*="Stop" i],button[aria-label*="Stop generating" i]'));
  }

  /* @mf-contract classifyAnchor
     ingress: unknown DOM node
     compute: require HTMLAnchorElement, resolved http(s) href, terminal .zip path or .zip download attribute
     egress: null or {href,expectedName,text}; no DOM mutation
     test-mode: replace DOM with structural mocks and enumerate missing/hostile properties
     production-mode: retain all boundary checks */
  function classifyAnchor(node) {
    if (!(node instanceof HTMLAnchorElement)) return null;
    const text = String(node.innerText || node.textContent || '').trim();
    const downloadHint = String(node.getAttribute('download') || '').trim();
    const textHint = (text.match(/([^\/\\\s]{1,240}\.zip)(?:\s|$)/i) || [])[1] || '';
    const rawHref = String(node.getAttribute('href') || node.href || '').trim();
    const core = Contract.classifyZipLink(String(node.href || rawHref).trim(), downloadHint || textHint);
    if (core) return {href: core.url, expectedName: core.expectedName, text: text.slice(0, 500), mode: 'download'};
    if (/^(sandbox:|blob:)/i.test(rawHref) && /\.zip$/i.test(downloadHint || textHint)) {
      return {href: rawHref, expectedName: downloadHint || textHint, text: text.slice(0, 500), mode: 'dom-click', node};
    }
    return null;
  }

  async function dispatchZip(link) {
    const key = `${location.href}\n${link.href}\n${link.expectedName}`;
    if (state.seenLinks.has(key)) return {ok: true, outcome: 'SEEN'};
    state.seenLinks.add(key);
    if (state.seenLinks.size > 5000) state.seenLinks = new Set(Array.from(state.seenLinks).slice(-2500));
    try {
      const response = await chrome.runtime.sendMessage({
        kind: 'mf.artifact.zip-link', href: link.href, expectedName: link.expectedName,
        linkText: link.text, mode: link.mode || 'download', originUrl: location.href, discoveredAt: new Date().toISOString()
      });
      if (response?.outcome === 'DOM_CLICK_REQUIRED' && link.node?.isConnected) {
        link.node.click();
        return {...response, clicked: true};
      }
      if (!response?.ok) state.seenLinks.delete(key);
      return response;
    } catch (error) {
      state.seenLinks.delete(key);
      return {ok: false, error: String(error?.message || error)};
    }
  }

  async function scanZipLinks(reason = 'mutation') {
    state.scans += 1;
    const anchors = Array.from(document.querySelectorAll('a[href]'));
    let discovered = 0;
    for (const anchor of anchors) {
      const link = classifyAnchor(anchor);
      if (!link) continue;
      discovered += 1;
      await dispatchZip(link);
    }
    return {ok: true, reason, anchors: anchors.length, discovered, scans: state.scans};
  }

  function returnAlreadyPresent(id) {
    if (typeof id !== 'string' || !id) return false;
    const nodes = Array.from(document.querySelectorAll('[data-message-author-role="user"], article'));
    return nodes.slice(-20).some(node => String(node?.innerText || node?.textContent || '').includes(id));
  }

  async function waitForReturnPostcondition(id, timeoutMs = 12000) {
    const deadline = Date.now() + Math.max(1000, Math.min(30000, Number(timeoutMs) || 12000));
    while (Date.now() < deadline) {
      if (returnAlreadyPresent(id)) return {ok: true, observed: 'USER_MESSAGE_CONTAINS_RETURN_ID'};
      await new Promise(resolve => setTimeout(resolve, 200));
    }
    return {ok: false, observed: 'SUBMISSION_POSTCONDITION_UNVERIFIED'};
  }

  /* @mf-contract boundedRuntimeDetails
     ingress: arbitrary ErrorEvent, rejection reason or adapter failure value
     compute: redact by selection, bound every string/number and attach replay generation metadata
     egress: plain serializable incident details with fixed maximum sizes
     test-mode: enumerate cyclic, hostile, missing and overlong error values
     extension-mode: forward only through authenticated background/broker path
     production-mode: retain all bounds and never include composer text, credentials or page storage */
  function boundedRuntimeDetails(kind, value = {}) {
    const details = value && typeof value === 'object' ? value : {value: String(value)};
    return {
      kind: String(kind || 'runtime-error').slice(0, 128),
      message: String(details.message || details.reason || '').slice(0, 8192),
      stack: String(details.stack || '').slice(0, 32768),
      source: String(details.source || '').slice(0, 4096),
      line: Number.isFinite(Number(details.line)) ? Number(details.line) : 0,
      column: Number.isFinite(Number(details.column)) ? Number(details.column) : 0,
      generation: `${location.href}:${performance.timeOrigin}`,
      contractVersion: VERSION,
      at: new Date().toISOString()
    };
  }

  function reportRuntimeIncident(kind, value) {
    const details = boundedRuntimeDetails(kind, value);
    chrome.runtime.sendMessage({kind: 'mf.runtime.incident', source: 'content-runtime-v2.0.1', originUrl: location.href, details}).catch(() => {});
  }

  /* @mf-contract deliverReturn
     ingress: untrusted return message from extension service worker
     compute: validate ID/text bounds, detect existing user message, preserve draft, refuse during generation, submit and observe DOM postcondition
     egress: submitted|duplicate|deferred result; never reports success from click alone
     test-mode: mock composer/send/rerender/crash windows and delayed postconditions
     extension-mode: bind live ChatGPT composer semantic adapter
     production-mode: retain all identity, draft, generation, size and observed-postcondition guards */
  async function deliverReturn(message) {
    const ret = message?.returnEnvelope;
    const id = typeof ret?.id === 'string' ? ret.id : '';
    const text = typeof message?.text === 'string' ? message.text : '';
    if (!id || !text || text.length > 2_000_000) return {ok: false, error: 'INVALID_RETURN_ENVELOPE'};
    if (state.deliveredReturns.has(id) || returnAlreadyPresent(id)) {
      state.deliveredReturns.add(id);
      return {ok: true, duplicate: true, id, observed: 'RETURN_ALREADY_PRESENT'};
    }
    if (isGenerating()) return {ok: false, deferred: true, error: 'ASSISTANT_GENERATING'};
    const composer = findComposer();
    if (!composer) return {ok: false, deferred: true, error: 'COMPOSER_NOT_FOUND'};
    const draft = composerText(composer);
    if (draft.trim()) return {ok: false, deferred: true, error: 'HUMAN_DRAFT_PRESENT', draftLength: draft.length};
    setComposer(composer, text);
    await new Promise(resolve => setTimeout(resolve, 120));
    const send = findSendButton();
    if (send) send.click();
    else {
      composer.dispatchEvent(new KeyboardEvent('keydown', {key: 'Enter', code: 'Enter', bubbles: true, cancelable: true}));
      composer.dispatchEvent(new KeyboardEvent('keyup', {key: 'Enter', code: 'Enter', bubbles: true, cancelable: true}));
    }
    const postcondition = await waitForReturnPostcondition(id);
    if (!postcondition.ok) return {ok: false, deferred: true, error: postcondition.observed, id, method: send ? 'button' : 'enter'};
    state.deliveredReturns.add(id);
    if (state.deliveredReturns.size > 2000) state.deliveredReturns = new Set(Array.from(state.deliveredReturns).slice(-1000));
    return {ok: true, submitted: true, id, method: send ? 'button' : 'enter', postcondition};
  }

  chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
    if (!message || typeof message !== 'object') return undefined;
    if (message.kind === 'mf.return.deliver') {
      deliverReturn(message).then(sendResponse, error => sendResponse({ok: false, error: String(error?.stack || error)}));
      return true;
    }
    if (message.kind === 'mf.artifact.scan-now') {
      scanZipLinks(message.reason || 'manual').then(sendResponse);
      return true;
    }
    return undefined;
  });

  let timer = 0;
  const observer = new MutationObserver(() => {
    clearTimeout(timer);
    timer = setTimeout(() => scanZipLinks('mutation').catch(() => {}), 150);
  });
  observer.observe(document.documentElement || document, {subtree: true, childList: true, attributes: true, attributeFilter: ['href', 'download']});
  window.addEventListener('pageshow', () => scanZipLinks('pageshow').catch(() => {}), {passive: true});
  setTimeout(() => scanZipLinks('boot').catch(() => {}), 500);
  setTimeout(() => chrome.runtime.sendMessage({kind: 'mf.locator.register-current'}).catch(() => {}), 1000);
})();
