// A small TextMate grammar for PromQL, which Shiki doesn't bundle: functions, aggregations and their
// modifiers, label matchers, strings, numbers and durations. Enough to read a query at a glance.
export const promql = {
  name: 'promql',
  scopeName: 'source.promql',
  aliases: ['prometheus'],
  patterns: [
    { include: '#comment' },
    { include: '#string' },
    { include: '#labels' },
    { include: '#range' },
    { name: 'keyword.control.promql', match: '\\b(by|without|on|ignoring|group_left|group_right|offset|bool|and|or|unless|atan2)\\b' },
    { name: 'support.function.aggregation.promql', match: '\\b(sum|min|max|avg|group|stddev|stdvar|count|count_values|bottomk|topk|quantile|limitk|limit_ratio)\\b(?=\\s*(\\(|by|without))' },
    { name: 'support.function.promql', match: '\\b[a-z_]+(?=\\s*\\()' },
    { name: 'constant.numeric.promql', match: '\\b\\d+(\\.\\d+)?([eE][-+]?\\d+)?\\b' },
    { name: 'keyword.operator.promql', match: '[-+*/%^]|==|!=|>=|<=|>|<' },
    { name: 'variable.other.metric.promql', match: '\\b[a-zA-Z_:][a-zA-Z0-9_:]*\\b' },
  ],
  repository: {
    comment: { name: 'comment.line.number-sign.promql', match: '#.*$' },
    string: { patterns: [
      { name: 'string.quoted.double.promql', begin: '"', end: '"', patterns: [{ name: 'constant.character.escape.promql', match: '\\\\.' }] },
      { name: 'string.quoted.single.promql', begin: "'", end: "'", patterns: [{ name: 'constant.character.escape.promql', match: '\\\\.' }] },
      { name: 'string.quoted.backtick.promql', begin: '`', end: '`' },
    ] },
    labels: { begin: '\\{', end: '\\}', beginCaptures: { 0: { name: 'punctuation.definition.labels.begin.promql' } }, endCaptures: { 0: { name: 'punctuation.definition.labels.end.promql' } }, patterns: [
      { include: '#string' },
      { name: 'entity.other.attribute-name.label.promql', match: '\\b[a-zA-Z_][a-zA-Z0-9_]*\\b' },
      { name: 'keyword.operator.match.promql', match: '=~|!~|!=|=' },
      { name: 'punctuation.separator.promql', match: ',' },
    ] },
    range: { name: 'meta.range.promql', match: '\\[(\\d+[smhdwy])+(:(\\d+[smhdwy])*)?\\]', captures: { 0: { name: 'constant.numeric.duration.promql' } } },
  },
};
