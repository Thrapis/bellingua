import type { Job } from "@/lib/api";

const URL = "/api/jobs/events";
const LOCK = "bellingua-jobs-stream";
const CHANNEL = "bellingua-jobs";

/**
 * Delivers the job list to onJobs, using one SSE connection for all tabs of
 * the origin: browsers allow only 6 HTTP/1.1 connections per host, so one
 * stream per tab starves searches once 6 tabs are open. The tab holding the
 * Web Lock owns the EventSource and relays each list over a BroadcastChannel;
 * when it closes, a waiting tab takes the lock over. Without Web Locks (an
 * insecure context) every tab opens its own stream. Returns a cleanup.
 */
export function followJobs(onJobs: (list: Job[]) => void): () => void {
  if (typeof navigator === "undefined" || !navigator.locks || typeof BroadcastChannel === "undefined") {
    return openStream(onJobs);
  }

  const bc = new BroadcastChannel(CHANNEL);
  const ac = new AbortController();
  let last: Job[] | null = null; // newest list, kept by the leader for tabs that join late
  let leading = false;

  bc.onmessage = (ev) => {
    const m = ev.data as { type: "jobs"; list: Job[] } | { type: "hello" };
    if (m.type === "jobs") onJobs(m.list);
    else if (leading && last) bc.postMessage({ type: "jobs", list: last });
  };
  bc.postMessage({ type: "hello" });

  navigator.locks
    .request(LOCK, { signal: ac.signal }, () => {
      leading = true;
      return new Promise<void>((release) => {
        const stop = openStream((list) => {
          last = list;
          onJobs(list);
          bc.postMessage({ type: "jobs", list });
        });
        ac.signal.addEventListener("abort", () => {
          stop();
          release();
        });
      });
    })
    .catch(() => {}); // aborted while still waiting for the lock

  return () => {
    ac.abort();
    bc.close();
  };
}

/** One EventSource that reconnects after errors. */
function openStream(onJobs: (list: Job[]) => void): () => void {
  let es: EventSource | null = null;
  let retry: ReturnType<typeof setTimeout> | undefined;
  const connect = () => {
    es = new EventSource(URL);
    es.onmessage = (ev) => onJobs(JSON.parse(ev.data));
    es.onerror = () => {
      es?.close();
      retry = setTimeout(connect, 2000);
    };
  };
  connect();
  return () => {
    es?.close();
    clearTimeout(retry);
  };
}
