import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import App from './App.jsx';

describe('continuation workspace shell', () => {
  it('renders continuation as a first-class workspace tab', () => {
    const markup = renderToStaticMarkup(createElement(App));

    expect(markup).toContain('title="续写"');
    expect(markup).not.toContain('title="导入"');
  });
});
