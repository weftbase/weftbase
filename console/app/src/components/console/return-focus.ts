import * as React from "react"

/** useReturnFocus: a dialog opened from a menu item gives the focus back
 * to the menu's trigger when it closes (the item it was opened from is
 * gone by then, so Radix would drop the focus on <body>). Put `ref` on
 * the trigger and `onCloseAutoFocus` on the DialogContent. */
export function useReturnFocus<T extends HTMLElement = HTMLButtonElement>() {
  const ref = React.useRef<T>(null)
  const onCloseAutoFocus = React.useCallback((e: Event) => {
    const el = ref.current
    if (!el || !el.isConnected) return
    e.preventDefault()
    el.focus()
  }, [])
  return { ref, onCloseAutoFocus }
}
