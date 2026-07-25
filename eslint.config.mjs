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
      // Complexity budgets, the JavaScript counterpart of gocyclo/gocognit/
      // maintidx on the Go side. The thresholds are the current ceiling, so they
      // are a no-regression gate rather than a cleanup backlog: nothing may get
      // worse than app.js already is. Lower them as the outliers are split —
      // renderOverview (complexity 121) and renderServiceDetail (183 lines) are
      // the two worth attacking first.
      complexity: ["error", 121],
      "max-lines-per-function": ["error", { max: 183, skipBlankLines: true, skipComments: true }],
      "max-params": ["error", 8],
      "max-depth": ["error", 4],
      "max-nested-callbacks": ["error", 3],
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
