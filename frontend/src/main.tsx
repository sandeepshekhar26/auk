import { render } from 'solid-js/web'
import App from './App'
// Self-hosted (no CDN/network dependency — this is an offline-capable
// desktop app) via @fontsource, which ships the actual woff2 files: Inter
// for UI text, JetBrains Mono for code/monospace. Only the weights the
// app actually uses (see tailwind font-* class usage) to keep the bundle
// lean rather than pulling in all 9 weights x italic per family.
import '@fontsource/inter/400.css'
import '@fontsource/inter/500.css'
import '@fontsource/inter/600.css'
import '@fontsource/inter/700.css'
import '@fontsource/jetbrains-mono/400.css'
import '@fontsource/jetbrains-mono/500.css'
import '@fontsource/jetbrains-mono/600.css'
import './index.css'

const root = document.getElementById('root')
if (!root) throw new Error('missing #root')

render(() => <App />, root)
