import { createRoot } from 'react-dom/client'
import { ThemeProvider } from './theme'
import App from './App'
import './styles.css'

createRoot(document.getElementById('root')!).render(
  <ThemeProvider><App /></ThemeProvider>
)