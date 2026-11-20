// Lets TypeScript accept `import "./app.css"`. esbuild turns that import
// into dist/app.css; TypeScript itself never reads the CSS.
declare module "*.css";
