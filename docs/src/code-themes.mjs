// Code blocks in the brand's palette (tokens.json), light and dark: three inks and the text colour.
// Keys, keywords and tags in teal (`brand`); strings, numbers and constants in amber (`accent-text`);
// comments and punctuation in `muted`; the rest in `code-text`. State colours stay for states only.
// Contrast on code-bg: light teal 5.75, amber 5.10, muted 6.25, text 15.64; dark teal 6.61, Lamp 9.17,
// muted 7.23, text 13.34.
const theme = (type, c) => ({
  name: `napwake-${type}`,
  type,
  colors: {
    'editor.background': c.bg,
    'editor.foreground': c.text,
    'editor.selectionBackground': c.select,
    'editorLineNumber.foreground': c.muted,
    'editorLineNumber.activeForeground': c.text,
    'terminal.background': c.bg,
    'terminal.foreground': c.text,
    focusBorder: c.teal,
  },
  tokenColors: [
    { scope: ['comment', 'punctuation.definition.comment'], settings: { foreground: c.muted, fontStyle: 'italic' } },
    { scope: ['punctuation', 'meta.brace', 'punctuation.separator', 'punctuation.definition.tag', 'keyword.operator'], settings: { foreground: c.muted } },
    {
      scope: ['keyword', 'storage', 'entity.name.tag', 'support.type.property-name', 'meta.object-literal.key', 'entity.other.attribute-name',
        'variable.other.normal', 'variable.other.special', 'variable.other.positional', 'variable.parameter', 'support.function', 'entity.name.function', 'variable.language'],
      settings: { foreground: c.teal },
    },
    { scope: ['string', 'string.unquoted', 'constant', 'constant.numeric', 'constant.language', 'constant.character', 'support.constant', 'markup.inline.raw'], settings: { foreground: c.amber } },
    { scope: ['string.unquoted.plain.out.yaml', 'string.unquoted.plain.in.yaml'], settings: { foreground: c.amber } },
    { scope: ['entity.name.tag.yaml'], settings: { foreground: c.teal } },
    { scope: ['punctuation.definition.string'], settings: { foreground: c.amber } },
    // shell: the command in teal, its arguments and options in the text colour, quoted strings amber
    { scope: ['string.unquoted.argument.shell', 'constant.other.option', 'constant.other.option.dash.shell', 'meta.argument.shell', 'string.unquoted.shell'], settings: { foreground: c.text } },
    { scope: ['constant.numeric.integer.shell', 'constant.numeric.shell'], settings: { foreground: c.text } },
    { scope: ['markup.bold'], settings: { fontStyle: 'bold' } },
    { scope: ['markup.italic'], settings: { fontStyle: 'italic' } },
  ],
});

export const napwakeLight = theme('light', { bg: '#eeeeea', text: '#0f1716', muted: '#4e5957', teal: '#14695f', amber: '#8a5a00', select: '#d3d3cb' });
export const napwakeDark = theme('dark', { bg: '#16201f', text: '#e3e7e4', muted: '#a3adaa', teal: '#4fb3a6', amber: '#f5b53f', select: '#263331' });
