import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router-dom'

import App from './App'
import './styles/index.css'

const query = new QueryClient({
  defaultOptions: {
    queries: {
      // Tools cache aggressively; retries on read paths would mask
      // real backend outages behind a spinner. One retry, short stale.
      retry: 1,
      staleTime: 5_000,
      refetchOnWindowFocus: false,
    },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <QueryClientProvider client={query}>
        <App />
      </QueryClientProvider>
    </BrowserRouter>
  </StrictMode>,
)
