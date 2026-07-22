import js from "@eslint/js";
import globals from "globals";

export default [
  {
    ignores: [
      "internal/web/index.html",
      "internal/web/src/vendor/**",
      "node_modules/**",
    ],
  },
  js.configs.recommended,
  {
    files: ["internal/web/src/**/*.js"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: globals.browser,
    },
    rules: {
      "no-empty": ["error", { allowEmptyCatch: true }],
      "no-constant-binary-expression": "error",
      "no-promise-executor-return": "error",
      "no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", caughtErrorsIgnorePattern: "^_" },
      ],
    },
  },
  {
    files: ["tests/web/**/*.{js,mjs}"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: { ...globals.browser, ...globals.node },
    },
    rules: {
      "no-empty": ["error", { allowEmptyCatch: true }],
      "no-constant-binary-expression": "error",
      "no-promise-executor-return": "error",
      "no-unused-vars": [
        "error",
        { argsIgnorePattern: "^_", caughtErrorsIgnorePattern: "^_" },
      ],
    },
  },
];
