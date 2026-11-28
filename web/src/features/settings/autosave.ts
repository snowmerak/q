export type AutoSaveState = "idle" | "pending" | "saving" | "saved" | "error";

export interface AutoSaverOptions<T, R = unknown> {
  delayMs?: number;
  save: (value: T) => Promise<R>;
  onStateChange?: (state: AutoSaveState) => void;
  onSuccess?: (result: R, value: T) => void;
  onError?: (err: unknown) => void;
}

export interface AutoSaver<T> {
  schedule: (value: T) => void;
  flush: (overrideValue?: T) => Promise<void>;
  dispose: () => void;
}

export function createAutoSaver<T, R = unknown>(
  options: AutoSaverOptions<T, R>,
): AutoSaver<T> {
  const delayMs = options.delayMs ?? 500;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let latestSeq = 0;
  let hasPending = false;
  let pendingValue: T | undefined;
  let inFlight: Promise<void> | null = null;
  let disposed = false;

  const emitState = (state: AutoSaveState) => {
    if (!disposed) {
      options.onStateChange?.(state);
    }
  };

  const clearTimer = () => {
    if (timer !== null) {
      clearTimeout(timer);
      timer = null;
    }
  };

  const drain = (): Promise<void> => {
    if (inFlight) {
      return inFlight;
    }
    inFlight = (async () => {
      while (hasPending) {
        const value = pendingValue as T;
        const seq = latestSeq;
        hasPending = false;
        pendingValue = undefined;
        emitState("saving");
        try {
          const res = await options.save(value);
          if (seq === latestSeq && !hasPending) {
            emitState("saved");
            if (!disposed) {
              options.onSuccess?.(res, value);
            }
          }
        } catch (err) {
          if (seq === latestSeq && !hasPending) {
            emitState("error");
            if (!disposed) {
              options.onError?.(err);
            }
          }
        }
      }
    })().finally(() => {
      inFlight = null;
      if (hasPending) {
        void drain();
      }
    });
    return inFlight;
  };

  const schedule = (value: T) => {
    if (disposed) return;
    latestSeq += 1;
    pendingValue = value;
    hasPending = true;
    emitState("pending");
    clearTimer();
    timer = setTimeout(() => {
      timer = null;
      void drain();
    }, delayMs);
  };

  const flush = (overrideValue?: T): Promise<void> => {
    clearTimer();
    if (!hasPending && overrideValue !== undefined) {
      latestSeq += 1;
      pendingValue = overrideValue;
      hasPending = true;
    }
    if (!hasPending) {
      return inFlight ?? Promise.resolve();
    }
    return drain();
  };

  const dispose = () => {
    clearTimer();
    disposed = true;
    if (hasPending) {
      void drain();
    }
  };

  return { schedule, flush, dispose };
}
