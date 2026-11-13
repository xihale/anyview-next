import { describe, expect, it, vi } from "vitest";
import { ApiError, createApiClient, type SchemeData } from "~/composables/useApi";

function makeDeferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function makeFakeFetcher() {
  const rawCalls: Array<{ url: string; opts?: { signal?: AbortSignal } }> = [];
  const plainCalls: Array<{ url: string; opts?: any }> = [];
  const rawPending: Array<{
    url: string;
    signal?: AbortSignal;
    deferred: ReturnType<typeof makeDeferred<{ _data: { code: number; data: SchemeData }; headers: Headers }>>;
  }> = [];

  const fetcher: any = (url: string, opts?: any) => {
    plainCalls.push({ url, opts });
    if (url === "/health") return Promise.resolve({ ok: true, mock: false });
    if (url.startsWith("/up/scheme-service/catalog/list")) return Promise.resolve({ code: 0, data: [] });
    return Promise.resolve({});
  };

  fetcher.raw = (url: string, opts?: { signal?: AbortSignal }) => {
    rawCalls.push({ url, opts });
    const deferred = makeDeferred<{ _data: { code: number; data: SchemeData }; headers: Headers }>();
    rawPending.push({ url, signal: opts?.signal, deferred });
    return deferred.promise;
  };

  return { fetcher, rawCalls, plainCalls, rawPending };
}

const sampleScheme = (eid: number): SchemeData => ({
  questionFullName: `q_${eid}`,
  questionId: eid,
  kind: 4,
  doc: "doc",
  language: 1,
  completes: [],
  stuCode: "",
});

describe("useApi 请求去重与取消", () => {
  it("并发相同 question GET 共享同一在途请求并透出 X-Relay-Stale", async () => {
    const { fetcher, rawCalls, rawPending } = makeFakeFetcher();
    const api = createApiClient(fetcher);

    const p1 = api.question(1001);
    const p2 = api.question(1001);
    expect(rawCalls).toHaveLength(1);
    expect(api._inflightCount()).toBe(1);

    rawPending[0].deferred.resolve({
      _data: { code: 0, data: sampleScheme(1001) },
      headers: new Headers({ "x-relay-stale": "1" }),
    });

    const [r1, r2] = await Promise.all([p1, p2]);
    expect(r1.stale).toBe(true);
    expect(r1.data?.questionId).toBe(1001);
    expect(r2).toEqual(r1);
    expect(api._inflightCount()).toBe(0);
  });

  it("多消费方共享请求时单方 abort 不中断另一方", async () => {
    const { fetcher, rawPending } = makeFakeFetcher();
    const api = createApiClient(fetcher);

    const ac1 = new AbortController();
    const ac2 = new AbortController();
    const p1 = api.question(1002, { signal: ac1.signal });
    const p2 = api.question(1002, { signal: ac2.signal });

    ac1.abort();
    await expect(p1).rejects.toThrow("Aborted");
    expect(rawPending[0].signal?.aborted).toBe(false);

    rawPending[0].deferred.resolve({
      _data: { code: 0, data: sampleScheme(1002) },
      headers: new Headers(),
    });
    const r2 = await p2;
    expect(r2.stale).toBe(false);
    expect(r2.data?.questionId).toBe(1002);
  });

  it("所有消费方均 abort 时中止底层请求并清理 inflight", async () => {
    const { fetcher, rawPending } = makeFakeFetcher();
    const api = createApiClient(fetcher);

    const ac = new AbortController();
    const p = api.question(1003, { signal: ac.signal });
    expect(rawPending[0].signal?.aborted).toBe(false);

    ac.abort();
    await expect(p).rejects.toThrow("Aborted");
    expect(rawPending[0].signal?.aborted).toBe(true);
    expect(api._inflightCount()).toBe(0);
  });

  it("并发 catalog 与 health 请求去重", async () => {
    const { fetcher, plainCalls } = makeFakeFetcher();
    const api = createApiClient(fetcher);

    await Promise.all([api.health(), api.health(), api.catalog(465, 381), api.catalog(465, 381)]);
    expect(plainCalls).toHaveLength(2);
  });

  it("429 响应触发后台预取冷却期", async () => {
    const fetcher: any = vi.fn().mockRejectedValue({ status: 429, data: { message: "冷却中" } });
    fetcher.raw = vi.fn().mockRejectedValue({ status: 429, data: { message: "冷却中" } });
    const api = createApiClient(fetcher);

    expect(api.isBackgroundCooling()).toBe(false);
    await expect(api.question(1004)).rejects.toBeInstanceOf(ApiError);
    expect(api.isBackgroundCooling()).toBe(true);
  });
});
