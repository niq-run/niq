import { useMemo, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { type WorkerInfo } from '../types'
import { mcpDeclared } from '../components/mcp-utils'
import { Chip, Pill } from '../components/mcp-ui'
import CreateMcpWorkerDialog from './CreateMcpWorkerDialog'
import McpWorkerPanel from './McpWorkerPanel'
import ViewHeader from '../components/ViewHeader'

interface McpViewProps {
  workers: WorkerInfo[]
  isMobile: boolean
  onRefresh?: () => void
}

// McpView is the roster of mcp workers. In niq an mcp worker is an ordinary
// worker (type "mcp") that connects to remote MCP servers and proxies their
// tools onto the bus; one worker can front SEVERAL servers, so the roster leads
// to a per-worker detail panel (McpWorkerPanel) rather than to a server list of
// its own. There is no MCP-specific backend: the detail panel drives the worker
// through its own mcp.* events (or, when it cannot answer, its declaration).
export default function McpView({ workers, isMobile, onRefresh }: McpViewProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const mcpWorkers = useMemo(() => workers.filter((w) => w.type === 'mcp'), [workers])
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  // Bumped by refresh: the open panel re-queries its worker (the live server set
  // and exposure are worker state, not part of the worker list).
  const [reloadSignal, setReloadSignal] = useState(0)

  const selected = mcpWorkers.find((w) => w.id === selectedId) ?? null
  // Desktop is master-detail and falls back to the first worker; mobile shows
  // the list until one is tapped.
  const panelWorker = isMobile ? selected : selected ?? mcpWorkers[0] ?? null

  const refresh = () => {
    setReloadSignal((k) => k + 1)
    onRefresh?.()
  }

  return (
    <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      {!isMobile ? (
        <ViewHeader
          title={t('mcp.title')}
          count={mcpWorkers.length}
          right={
            <>
              <Pill onClick={refresh} colors={colors} label={t('workers.refresh')} />
              <Pill onClick={() => setShowCreate(true)} colors={colors} label={t('mcp.new')} accent />
            </>
          }
        />
      ) : (
        <div style={{ display: 'flex', gap: 8, padding: '12px 16px 8px', flexShrink: 0 }}>
          <Pill onClick={() => setShowCreate(true)} colors={colors} label={t('mcp.new')} accent grow />
          <Pill onClick={refresh} colors={colors} label={t('workers.refresh')} />
        </div>
      )}

      <div style={{ flex: 1, minWidth: 0, display: 'flex', overflow: 'hidden' }}>
        {mcpWorkers.length === 0 ? (
          <EmptyState onCreate={() => setShowCreate(true)} />
        ) : (
          <>
            {(!isMobile || !panelWorker) && (
              <div
                style={{
                  width: isMobile ? '100%' : 300,
                  flexShrink: 0,
                  minHeight: 0,
                  overflowY: 'auto',
                  borderRight: isMobile ? undefined : '1px solid ' + colors.border,
                }}
              >
                {mcpWorkers.map((w) => (
                  <RailRow
                    key={w.id}
                    worker={w}
                    selected={panelWorker?.id === w.id}
                    onSelect={() => setSelectedId(w.id)}
                  />
                ))}
              </div>
            )}
            {panelWorker && (
              <div style={{ flex: 1, minWidth: 0, minHeight: 0, overflowY: 'auto' }}>
                {isMobile && (
                  <div style={{ padding: '8px 16px', borderBottom: '1px solid ' + colors.border }}>
                    <span
                      onClick={() => setSelectedId(null)}
                      className="btn-hover"
                      style={{
                        cursor: 'pointer',
                        color: colors.accent,
                        fontSize: fontSizes.sm,
                        border: '1px solid ' + colors.border,
                        borderRadius: 3,
                        padding: '3px 10px',
                        userSelect: 'none',
                      }}
                    >
                      {'\u2190 ' + t('mcp.back')}
                    </span>
                  </div>
                )}
                <McpWorkerPanel
                  key={panelWorker.id}
                  worker={panelWorker}
                  isMobile={isMobile}
                  onRefresh={refresh}
                  reloadSignal={reloadSignal}
                />
              </div>
            )}
          </>
        )}
      </div>

      <CreateMcpWorkerDialog
        open={showCreate}
        onClose={() => setShowCreate(false)}
        onCreated={(id) => {
          setShowCreate(false)
          setSelectedId(id)
          onRefresh?.()
        }}
      />
    </div>
  )
}

// RailRow is one mcp worker in the left roster: status, id and its declared
// mode. The live server set lives in the panel (it needs a bus round trip).
function RailRow({ worker, selected, onSelect }: { worker: WorkerInfo; selected: boolean; onSelect: () => void }) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const online = worker.online !== false
  const decl = useMemo(() => mcpDeclared(worker.params), [worker.params])
  return (
    <div
      onClick={onSelect}
      style={{
        padding: '10px 14px',
        borderBottom: '1px solid ' + colors.border,
        cursor: 'pointer',
        userSelect: 'none',
        background: selected ? colors.bgLight : 'transparent',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
        <span
          title={online ? t('worker.online') : t('worker.offline')}
          style={{ width: 7, height: 7, borderRadius: '50%', flexShrink: 0, background: online ? colors.toolCompleted : colors.textDimmed }}
        />
        <strong
          style={{
            color: colors.text,
            fontSize: fontSizes.md,
            minWidth: 0,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {worker.id}
        </strong>
      </div>
      {worker.description && (
        <div
          style={{
            marginTop: 3,
            color: colors.textDim,
            fontSize: fontSizes.xs,
            lineHeight: 1.4,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {worker.description}
        </div>
      )}
      <div style={{ display: 'flex', gap: 6, marginTop: 6, flexWrap: 'wrap' }}>
        <Chip colors={colors}>{decl.fixed ? t('mcp.mode.fixed') : t('mcp.mode.dynamic')}</Chip>
        {/* The declared server count only: the live set (and exposure) belong to
            the panel, which asks the worker. */}
        {decl.servers.length > 0 && <Chip colors={colors}>{t('mcp.serverCount', { n: decl.servers.length })}</Chip>}
      </div>
    </div>
  )
}

function EmptyState({ onCreate }: { onCreate: () => void }) {
  const { colors } = useTheme()
  const { t } = useI18n()
  return (
    <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 32 }}>
      <div style={{ maxWidth: 560, padding: '20px 22px', background: colors.detailBg, border: '1px solid ' + colors.detailBorder, borderRadius: 8 }}>
        <div style={{ color: colors.text, fontSize: fontSizes.lg, marginBottom: 8 }}>{t('mcp.empty.title')}</div>
        <div style={{ color: colors.textDim, fontSize: fontSizes.sm, lineHeight: 1.7, marginBottom: 14 }}>{t('mcp.empty.desc')}</div>
        <Pill onClick={onCreate} colors={colors} label={t('mcp.empty.create')} accent />
      </div>
    </div>
  )
}
