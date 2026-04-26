// Minimal PKCE auth code flow for OIDC.
// Tokens are stored in sessionStorage -- they are lost on tab close,
// which forces re-auth rather than silently using a stale token.

const TOKEN_KEY = 'agentq_token';
const VERIFIER_KEY = 'agentq_pkce_verifier';
const STATE_KEY = 'agentq_pkce_state';

export interface TokenInfo {
  accessToken: string;
  expiresAt: number; // ms since epoch
}

export function getToken(): TokenInfo | null {
  const raw = sessionStorage.getItem(TOKEN_KEY);
  if (!raw) return null;
  const info = JSON.parse(raw) as TokenInfo;
  if (Date.now() >= info.expiresAt) {
    sessionStorage.removeItem(TOKEN_KEY);
    return null;
  }
  return info;
}

export function clearToken() {
  sessionStorage.removeItem(TOKEN_KEY);
}

// --- PKCE helpers -----------------------------------------------------------

function randomBase64url(bytes: number): string {
  const buf = crypto.getRandomValues(new Uint8Array(bytes));
  return btoa(String.fromCharCode(...buf))
    .replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
}

async function sha256base64url(plain: string): Promise<string> {
  const enc = new TextEncoder().encode(plain);
  const digest = await crypto.subtle.digest('SHA-256', enc);
  return btoa(String.fromCharCode(...new Uint8Array(digest)))
    .replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
}

// --- Public API -------------------------------------------------------------

export async function startLogin(issuer: string, clientId: string) {
  const verifier = randomBase64url(48);
  const challenge = await sha256base64url(verifier);
  const state = randomBase64url(16);

  sessionStorage.setItem(VERIFIER_KEY, verifier);
  sessionStorage.setItem(STATE_KEY, state);

  const redirectUri = window.location.origin + '/callback';
  const url = new URL(issuer.replace(/\/$/, '') + '/oauth/v2/authorize');
  url.searchParams.set('response_type', 'code');
  url.searchParams.set('client_id', clientId);
  url.searchParams.set('redirect_uri', redirectUri);
  url.searchParams.set('scope', 'openid profile email');
  url.searchParams.set('code_challenge', challenge);
  url.searchParams.set('code_challenge_method', 'S256');
  url.searchParams.set('state', state);
  window.location.href = url.toString();
}

// Call this when the browser lands on /callback?code=...&state=...
// Returns true and stores the token on success; false on failure.
export async function handleCallback(issuer: string, clientId: string): Promise<boolean> {
  const params = new URLSearchParams(window.location.search);
  const code = params.get('code');
  const state = params.get('state');
  const storedState = sessionStorage.getItem(STATE_KEY);
  const verifier = sessionStorage.getItem(VERIFIER_KEY);

  if (!code || !state || state !== storedState || !verifier) return false;

  sessionStorage.removeItem(STATE_KEY);
  sessionStorage.removeItem(VERIFIER_KEY);

  const redirectUri = window.location.origin + '/callback';
  const resp = await fetch(issuer.replace(/\/$/, '') + '/oauth/v2/token', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({
      grant_type: 'authorization_code',
      client_id: clientId,
      code,
      redirect_uri: redirectUri,
      code_verifier: verifier,
    }),
  });

  if (!resp.ok) return false;

  const data = await resp.json() as { access_token: string; expires_in: number };
  sessionStorage.setItem(TOKEN_KEY, JSON.stringify({
    accessToken: data.access_token,
    expiresAt: Date.now() + data.expires_in * 1000,
  } satisfies TokenInfo));

  return true;
}
