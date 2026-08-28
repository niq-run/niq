import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter'
import { vscDarkPlus } from 'react-syntax-highlighter/dist/esm/styles/prism'
import { oneLight } from 'react-syntax-highlighter/dist/esm/styles/prism'

export function makeMdComponents(dark: boolean, colors: any) {
  const hlStyle = dark ? vscDarkPlus : oneLight
  return {
    code({ className, children, ...props }: any) {
      const match = /language-(\w+)/.exec(className || '')
      if (!match) {
        return <code className={className} {...props}>{children}</code>
      }
      return (
        <SyntaxHighlighter style={hlStyle} language={match[1]} PreTag="div">
          {String(children).replace(/\n$/, '')}
        </SyntaxHighlighter>
      )
    },
    hr() {
      return <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '12px 0', opacity: 0.4 }} />
    },
    // Links use the theme accent instead of the browser-default blue, which is
    // hard to read on the dark background.
    a({ href, children, ...props }: any) {
      return (
        <a
          href={href}
          {...props}
          style={{
            color: colors.accent,
            textDecoration: 'underline',
            textDecorationStyle: 'dotted',
            textUnderlineOffset: 3,
          }}
        >
          {children}
        </a>
      )
    },
  }
}
