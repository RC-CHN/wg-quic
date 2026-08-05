import type { RuleSetRule } from 'webpack';

export const rules: RuleSetRule[] = [
  {
    test: /\.tsx?$/,
    exclude: /(node_modules|\.webpack)/,
    use: {
      loader: 'ts-loader',
    },
  },
];
