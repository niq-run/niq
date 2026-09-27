import { createRoot } from 'react-dom/client'
import { ThemeProvider } from './theme'
import { LanguageProvider } from './i18n'
import App from './App'
import './styles.css'

createRoot(document.getElementById('root')!).render(
  <LanguageProvider><ThemeProvider><App /></ThemeProvider></LanguageProvider>
)