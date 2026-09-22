import { http } from '@/lib/http.ts';

// The NetBird CLI is slow on riscv64: `netbird up` waits up to 70s for a login
// URL, and `status` may chain three one-minute windows when it restarts the
// daemon on timeout. The shared 60s default fires before the server answers, so
// these calls get their own budget. `http.get` takes no per-request config, so
// status goes through `http.request` instead of widening the shared wrapper.
const SLOW_CALL_TIMEOUT = 180 * 1000;

// Install downloads the client (~14 MB compressed) over whatever uplink the
// device has, so it gets a far larger budget than the CLI calls.
const DOWNLOAD_TIMEOUT = 10 * 60 * 1000;

export function install() {
  return http.post('/api/extensions/netbird/install', undefined, {
    timeout: DOWNLOAD_TIMEOUT
  });
}

export function uninstall() {
  return http.post('/api/extensions/netbird/uninstall');
}

export function getStatus() {
  return http.request({
    method: 'get',
    url: '/api/extensions/netbird/status',
    timeout: SLOW_CALL_TIMEOUT
  });
}

export function login() {
  return http.post('/api/extensions/netbird/login', undefined, {
    timeout: SLOW_CALL_TIMEOUT
  });
}

export function start() {
  return http.post('/api/extensions/netbird/start', undefined, {
    timeout: SLOW_CALL_TIMEOUT
  });
}

export function restart() {
  return http.post('/api/extensions/netbird/restart', undefined, {
    timeout: SLOW_CALL_TIMEOUT
  });
}

export function stop() {
  return http.post('/api/extensions/netbird/stop');
}

export function down() {
  return http.post('/api/extensions/netbird/down');
}
