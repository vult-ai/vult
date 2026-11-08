// Phosphor publishes a CommonJS entry Node cannot load (its package is an ES
// module), so test bundles use its ES build, as the app's own build does.
export const phosphorPlugin = {
  name: "phosphor-es",
  setup(build) {
    build.onResolve({ filter: /^@phosphor-icons\/react$/ }, () => ({
      path: new URL("../../node_modules/@phosphor-icons/react/dist/index.es.js", import.meta.url).pathname,
    }));
  },
};
