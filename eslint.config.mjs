import js from "@eslint/js";
import globals from "globals";
import sonarjs from "eslint-plugin-sonarjs";

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
    plugins: { sonarjs },
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
      // worse than app.js already is. Lower them as the outliers are split;
      // restoreUIState (complexity 80) and initStaticHandlers (146 lines) are
      // the current ceilings.
      complexity: ["error", 80],
      "max-lines-per-function": ["error", { max: 146, skipBlankLines: true, skipComments: true }],
      "max-params": ["error", 8],
      "max-depth": ["error", 4],
      "max-nested-callbacks": ["error", 3],
      // Duplication and regex guards, the counterpart of dupl and the ReDoS
      // half of gosec on the Go side. They report nothing today except the
      // three bounded-input regexes suppressed at their call sites; they are
      // here so a copy-pasted function or a regex over daemon-independent
      // input cannot land unnoticed.
      "sonarjs/no-identical-functions": "error",
      "sonarjs/no-identical-conditions": "error",
      "sonarjs/no-identical-expressions": "error",
      "sonarjs/no-all-duplicated-branches": "error",
      "sonarjs/super-linear-regex": "error",
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
