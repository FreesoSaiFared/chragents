;(() => {
  'use strict';
  const VERSION = '2.0.1';
  if (globalThis.MFContractCoreV200?.version === VERSION) return;

  /** @mf-contract canonicalChatIdentity
   * ingress: unknown URL-like value
   * compute: parse only http(s), derive stable ChatGPT project+conversation identity, strip query/fragment
   * egress: null | frozen ChatIdentity
   * test-mode: export through globalThis in Node VM and fuzz with deterministic C corpus
   * extension-mode: same function object consumed by background/content actors
   * production-mode: retain all trust-boundary checks
   */
  function canonicalChatIdentity(raw) {
    if (typeof raw !== 'string' || raw.length < 8 || raw.length > 8192) return null;
    let url;
    try { url = new URL(raw); } catch (_) { return null; }
    if (!['https:', 'http:'].includes(url.protocol)) return null;
    const host = url.hostname.toLowerCase();
    if (!['chatgpt.com', 'chat.openai.com'].includes(host)) return null;
    const parts = url.pathname.split('/').filter(Boolean).map(part => {
      try { return decodeURIComponent(part); } catch (_) { return part; }
    });
    let projectId = '';
    let conversationId = '';
    for (let i = 0; i < parts.length; i += 1) {
      if (parts[i].startsWith('g-p-')) projectId = parts[i];
      if (parts[i] === 'c' && i + 1 < parts.length) { conversationId = parts[i + 1]; break; }
    }
    if (!conversationId || conversationId.length > 256) return null;
    const result = Object.freeze({
      key: `chat://chatgpt/${projectId ? `${projectId}/` : ''}${conversationId}`,
      canonicalUrl: `https://chatgpt.com/${projectId ? `g/${encodeURIComponent(projectId)}/` : ''}c/${encodeURIComponent(conversationId)}`,
      provider: 'chatgpt', projectId, conversationId
    });
    if (!result.key.startsWith('chat://chatgpt/') || !result.canonicalUrl.startsWith('https://chatgpt.com/')) return null;
    return result;
  }

  /** @mf-contract classifyZipLink
   * ingress: unknown href plus optional filename hint
   * compute: parse http(s), reject credentials, require terminal .zip path or explicit .zip hint
   * egress: null | frozen {url,expectedName}
   * test-mode: fuzz schemes, encoding, credentials, path/query/fragment, long and malformed names
   * extension-mode: same function object used by page and service-worker adapters
   * production-mode: retain all trust-boundary checks
   */
  function classifyZipLink(rawHref, hint = '') {
    if (typeof rawHref !== 'string' || rawHref.length < 8 || rawHref.length > 16384) return null;
    let url;
    try { url = new URL(rawHref); } catch (_) { return null; }
    if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password) return null;
    const hinted = typeof hint === 'string' ? hint.trim() : '';
    const decodedParts = [hinted];
    for (const part of [url.pathname, url.search, url.hash]) {
      try { decodedParts.push(decodeURIComponent(part)); } catch (_) { decodedParts.push(part); }
    }
    let expectedName = '';
    for (const part of decodedParts) {
      const matches = String(part || '').match(/(?:^|[\/?#&=])([^\/?#&=\\\0]{1,240}\.zip)(?=$|[\/?#&=])/i);
      if (matches) { expectedName = matches[1]; break; }
    }
    if (!expectedName || expectedName.length > 240 || /[\\/\0]/.test(expectedName)) return null;
    const result = Object.freeze({url: url.href, expectedName});
    return /\.zip$/i.test(result.expectedName) && /^https?:/.test(result.url) ? result : null;
  }

  /** @mf-contract validateReturnEnvelope
   * ingress: unknown broker-spool value
   * compute: validate ID, kind, origin URL, canonical conversation binding and object payload
   * egress: null | frozen normalized return
   * test-mode: enumerate malformed objects, state transitions, identity mismatches and metadata bounds
   * extension-mode: consume only READY envelopes from authenticated broker spool
   * production-mode: permanent; this is a trust boundary
   */
  function validateReturnEnvelope(value) {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
    const id = typeof value.id === 'string' ? value.id : '';
    const kind = typeof value.kind === 'string' ? value.kind : '';
    const originUrl = typeof value.originUrl === 'string' ? value.originUrl : '';
    if (!/^return-[A-Za-z0-9_.-]{3,256}$/.test(id) || !kind || !originUrl) return null;
    const identity = canonicalChatIdentity(originUrl);
    if (!identity) return null;
    const payload = value.payload && typeof value.payload === 'object' && !Array.isArray(value.payload) ? value.payload : {};
    const conversationKey = typeof value.conversationKey === 'string' && value.conversationKey ? value.conversationKey : identity.key;
    if (conversationKey !== identity.key) return null;
    const state = typeof value.state === 'string' && value.state ? value.state : 'READY';
    if (state !== 'READY') return null;
    const tabActorId = typeof value.tabActorId === 'string' ? value.tabActorId.slice(0, 512) : '';
    const tabId = Number.isInteger(value.tabId) && value.tabId > 0 ? value.tabId : 0;
    return Object.freeze({id, kind, originUrl, conversationKey, tabActorId, tabId, state, payload});
  }

  function safeArray(value) { return Array.isArray(value) ? value : []; }
  function safeObject(value) { return value && typeof value === 'object' && !Array.isArray(value) ? value : {}; }

  globalThis.MFContractCoreV200 = Object.freeze({
    version: VERSION,
    canonicalChatIdentity,
    classifyZipLink,
    validateReturnEnvelope,
    safeArray,
    safeObject
  });
})();
