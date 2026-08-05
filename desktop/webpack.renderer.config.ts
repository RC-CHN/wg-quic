import type { Configuration } from 'webpack';
import { rules } from './webpack.rules';

export const rendererConfig: Configuration = {
  devtool: 'source-map',
  module: {
    rules: [
      ...rules,
      {
        test: /\.css$/,
        use: ['style-loader', 'css-loader'],
      },
    ],
  },
  resolve: {
    extensions: ['.js', '.ts', '.css'],
  },
};
