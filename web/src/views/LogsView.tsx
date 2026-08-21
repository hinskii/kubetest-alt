import { useParams } from 'react-router-dom'
import { api } from '../api/client'
import { LogsViewer } from '../components/LogsViewer'
import { PageHeader } from '../components/PageHeader'

// LogsView is a full-height wrapper around LogsViewer. Kept thin
// on purpose — the state-machine and stream handling live in the
// component, this page just wires the id → ws url.
export default function LogsView() {
  const { id = '' } = useParams()
  return (
    <div className="flex flex-col min-h-0 h-full">
      <PageHeader
        eyebrow="logs"
        title={<span className="font-mono">{id}</span>}
        backTo={{ to: `/runs/${encodeURIComponent(id)}`, label: 'run detail' }}
      />
      <div className="flex-1 min-h-0">
        <LogsViewer url={api.logsUrl(id)} />
      </div>
    </div>
  )
}
