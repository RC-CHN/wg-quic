import type { Configuration } from 'webpack';
import { rules } from './webpack.rules';

export const mainConfig: Configuration = {
  entry: './src/main.ts',
  devtool: 'source-map',
  module: {
    rules,
  },
  resolve: {
    extensions: ['.js', '.ts', '.json'],
  },
};
