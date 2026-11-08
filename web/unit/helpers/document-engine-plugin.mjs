export const documentEnginePlugin = {
  name: "document-decoder-dom-fixture",
  setup(build) {
    build.onResolve({ filter: /\/document-engine$/ }, () => ({
      path: new URL("./document-engine.mjs", import.meta.url).pathname,
      external: true,
    }));
  },
};
