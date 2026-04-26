export interface AuthConfig {
  enabled: boolean;
  issuer?: string;
  client_id?: string;
}

export interface AppConfig {
  auth: AuthConfig;
}

let cached: AppConfig | null = null;

export async function getConfig(): Promise<AppConfig> {
  if (cached) return cached;
  const res = await fetch('/api/v1/config');
  if (!res.ok) {
    // Assume no auth if config endpoint is unreachable.
    cached = { auth: { enabled: false } };
  } else {
    cached = await res.json() as AppConfig;
  }
  return cached;
}
