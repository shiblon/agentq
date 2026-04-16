// Minimal hash-based router. Routes like "#/sessions/abc123" are matched
// against patterns like "/sessions/:id". No dependency on react-router.

import { useState, useEffect } from 'react';

export interface RouteMatch {
  path: string;       // raw hash path, e.g. "/sessions/abc"
  params: Record<string, string>;
}

function parseHash(): string {
  const h = window.location.hash;
  return h.startsWith('#') ? h.slice(1) || '/' : '/';
}

function matchPattern(pattern: string, path: string): Record<string, string> | null {
  const patParts = pattern.split('/');
  const pathParts = path.split('/');
  if (patParts.length !== pathParts.length) return null;
  const params: Record<string, string> = {};
  for (let i = 0; i < patParts.length; i++) {
    if (patParts[i].startsWith(':')) {
      params[patParts[i].slice(1)] = decodeURIComponent(pathParts[i]);
    } else if (patParts[i] !== pathParts[i]) {
      return null;
    }
  }
  return params;
}

export function useRoute(): RouteMatch {
  const [path, setPath] = useState(parseHash);
  useEffect(() => {
    const handler = () => setPath(parseHash());
    window.addEventListener('hashchange', handler);
    return () => window.removeEventListener('hashchange', handler);
  }, []);
  return { path, params: {} };
}

export function matchRoute(path: string, pattern: string): Record<string, string> | null {
  return matchPattern(pattern, path);
}

export function navigate(to: string) {
  window.location.hash = to;
}
