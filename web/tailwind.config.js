/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    // Reset the palette: only the tokens we defined in the design plan.
    // Anything reaching for `bg-purple-500` outside these means we drifted.
    colors: {
      transparent: 'transparent',
      current: 'currentColor',
      ink: '#0B0F14',
      bone: '#EDE7DA',
      // Ink at low opacity for hairlines + subtle backgrounds.
      rule: 'rgba(11, 15, 20, 0.14)',
      subtle: 'rgba(11, 15, 20, 0.55)',
      hover: 'rgba(11, 15, 20, 0.04)',
      band: 'rgba(11, 15, 20, 0.025)',
      // Phase colors — the ONE place color earns its keep.
      pass: '#17614A',
      fail: '#B02A2A',
      err: '#C46B14',
      run: '#1E4A99',
      pend: '#6B6255',
      white: '#FFFFFF',
    },
    fontFamily: {
      // Monospace-first is the identity. Plex Mono is on jsdelivr (see index.html).
      mono: [
        'IBM Plex Mono',
        'ui-monospace',
        'SFMono-Regular',
        'Menlo',
        'Consolas',
        'monospace',
      ],
    },
    fontSize: {
      xs: ['0.71rem', { lineHeight: '1.35' }],
      sm: ['0.82rem', { lineHeight: '1.4' }],
      base: ['0.9rem', { lineHeight: '1.5' }],
      md: ['1rem', { lineHeight: '1.4' }],
      lg: ['1.25rem', { lineHeight: '1.25' }],
      xl: ['1.75rem', { lineHeight: '1.15' }],
    },
    letterSpacing: {
      normal: '-0.005em',
      wide: '0.06em', // used for eyebrow labels
    },
    borderRadius: {
      none: '0',
      sm: '2px',
    },
    extend: {},
  },
  corePlugins: {
    // No shadows, no gradients — tools don't need them.
    boxShadow: false,
    ringOffsetWidth: false,
  },
}
