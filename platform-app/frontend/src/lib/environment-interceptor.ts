import type { Interceptor } from "@connectrpc/connect";

export function createEnvironmentInterceptor(
  getHeaders: () => Record<string, string>,
): Interceptor {
  return (next) => async (req) => {
    const headers = getHeaders();
    for (const [name, value] of Object.entries(headers)) {
      if (value) {
        req.header.set(name, value);
      }
    }
    return next(req);
  };
}

