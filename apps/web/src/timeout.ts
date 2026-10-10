/** How long a list request may hang before it is given up on. */
export const REQUEST_LIMIT = 15_000;

/**
 * A promise that gives up. A request that never answers settles as a failure
 * after `ms`, so the screen can offer to try again rather than wait forever.
 * The request itself is not cancelled: its answer is just no longer waited for.
 */
export function within<T>(promise: Promise<T>, ms = REQUEST_LIMIT): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("The platform took too long to answer.")), ms);
    promise.then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      (error) => {
        clearTimeout(timer);
        reject(error);
      },
    );
  });
}
