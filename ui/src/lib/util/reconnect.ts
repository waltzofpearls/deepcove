// What to do when a terminal's WebSocket closes.
//
//   retry     open a new socket shortly, the session is probably still starting
//   reattach  check the session is up first, then attach from scratch
//   give-up   show "disconnected" and stop
export type ReconnectAction = 'retry' | 'reattach' | 'give-up';

export function reconnectAction(o: {
  // everOpened: this socket completed its handshake.
  everOpened: boolean;
  // everConnected: some socket has worked since the terminal mounted.
  everConnected: boolean;
  lifetimeMs: number;
  attempts: number;
  maxAttempts: number;
  earlyCloseThresholdMs: number;
}): ReconnectAction {
  // A stream that ran for a while and then dropped means the server went away, usually a restart.
  // Quick retries would just fail against a server that is not listening yet.
  if (o.everOpened && o.lifetimeMs >= o.earlyCloseThresholdMs) return 'reattach';
  if (o.attempts < o.maxAttempts) return 'retry';
  // Quick retries are used up. Keep trying the slow way if this terminal ever worked; a session that
  // has never attached at all is a real failure worth showing.
  return o.everConnected ? 'reattach' : 'give-up';
}
