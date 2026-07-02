/** @type {import('tailwindcss').Config} */

// All colors reference the semantic CSS variables defined in src/index.css.
// Components use these names exclusively — never raw palette colors — so
// themes (dark/light/future community themes) swap by changing variables,
// with zero component changes.
const token = (name) => `rgb(var(--color-${name}) / <alpha-value>)`

export default {
  content: ['./index.html', './src/**/*.{js,ts,jsx,tsx}'],
  theme: {
    extend: {
      colors: {
        app: token('app'),
        surface: token('surface'),
        field: token('field'),
        raised: token('raised'),
        elevated: token('elevated'),
        edge: token('edge'),
        'edge-strong': token('edge-strong'),
        'edge-soft': token('edge-soft'),
        ink: token('ink'),
        'ink-dim': token('ink-dim'),
        'ink-muted': token('ink-muted'),
        'ink-faint': token('ink-faint'),
        accent: token('accent'),
        'accent-hover': token('accent-hover'),
        'accent-fg': token('accent-fg'),
        'accent-contrast': token('accent-contrast'),
        danger: token('danger'),
        'danger-bg': token('danger-bg'),
        'danger-edge': token('danger-edge'),
        warn: token('warn'),
        'warn-edge': token('warn-edge'),
        info: token('info'),
      },
      fontFamily: {
        mono: ['"JetBrains Mono"', 'ui-monospace', 'SFMono-Regular', 'monospace'],
      },
    },
  },
  plugins: [],
}
