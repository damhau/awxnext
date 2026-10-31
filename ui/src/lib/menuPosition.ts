/** Where a menu opened from a button goes, in viewport coordinates. */
export interface Box {
  top: number
  bottom: number
  left: number
  right: number
}

/**
 * placeMenu puts a menu of size `menu` under its trigger, its right edge on
 * the trigger's (align 'right') or its left edge (align 'left'). It opens
 * upwards when it does not fit below but fits above, and stays `margin` away
 * from the viewport's edges.
 */
export function placeMenu(
  trigger: Box,
  menu: { width: number; height: number },
  viewport: { width: number; height: number },
  align: 'left' | 'right' = 'right',
  gap = 4,
  margin = 8,
): { top: number; left: number } {
  let left = align === 'right' ? trigger.right - menu.width : trigger.left
  left = Math.max(margin, Math.min(left, viewport.width - margin - menu.width))

  const below = trigger.bottom + gap
  const above = trigger.top - gap - menu.height
  let top = below
  if (below + menu.height > viewport.height - margin) {
    top = above >= margin ? above : Math.max(margin, viewport.height - margin - menu.height)
  }
  return { top, left }
}
