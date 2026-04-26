import { useRoute, matchRoute, navigate } from './router';
import { Nav } from './components/Nav';
import { Sessions } from './pages/Sessions';
import { SessionDetail } from './pages/SessionDetail';
import { Review } from './pages/Review';
import { Agents } from './pages/Agents';
import { Queues } from './pages/Queues';
import { useEffect, useState } from 'react';
import { getConfig, type AppConfig } from './config';
import { getToken, handleCallback, startLogin } from './auth';

type AuthState = 'loading' | 'callback' | 'authed' | 'unauthed';

export default function App() {
  const { path } = useRoute();
  const [config, setConfig] = useState<AppConfig | null>(null);
  const [authState, setAuthState] = useState<AuthState>('loading');

  useEffect(() => {
    getConfig().then(async cfg => {
      setConfig(cfg);

      if (!cfg.auth.enabled) {
        setAuthState('authed');
        return;
      }

      // Handle OAuth callback: ?code=...&state=... in the URL.
      if (window.location.search.includes('code=')) {
        setAuthState('callback');
        const ok = await handleCallback(cfg.auth.issuer!, cfg.auth.client_id!);
        // Strip query params and redirect to sessions.
        window.history.replaceState({}, '', window.location.pathname);
        setAuthState(ok ? 'authed' : 'unauthed');
        if (ok) navigate('/sessions');
        return;
      }

      setAuthState(getToken() ? 'authed' : 'unauthed');
    });
  }, []);

  // Re-check auth state when token is cleared (e.g. after a 401).
  useEffect(() => {
    if (authState === 'authed' && config?.auth.enabled && !getToken()) {
      setAuthState('unauthed');
    }
  });

  // Redirect root to /sessions once authed.
  useEffect(() => {
    if (authState === 'authed' && path === '/') navigate('/sessions');
  }, [path, authState]);

  if (authState === 'loading' || authState === 'callback') {
    return <div className="app-loading">Loading…</div>;
  }

  if (authState === 'unauthed') {
    return (
      <div className="app-login">
        <h1>agentq</h1>
        <button
          className="btn-primary"
          onClick={() => startLogin(config!.auth.issuer!, config!.auth.client_id!)}
        >
          Sign in
        </button>
      </div>
    );
  }

  let page: React.ReactNode = null;
  const sessionDetail = matchRoute(path, '/sessions/:id');
  if (sessionDetail) {
    page = <SessionDetail id={sessionDetail.id} />;
  } else if (path === '/sessions') {
    page = <Sessions />;
  } else if (path === '/review') {
    page = <Review />;
  } else if (path === '/agents') {
    page = <Agents />;
  } else if (path === '/queues') {
    page = <Queues />;
  }

  return (
    <div className="app">
      <Nav currentPath={path} />
      <main className="app-main">
        {page}
      </main>
    </div>
  );
}
