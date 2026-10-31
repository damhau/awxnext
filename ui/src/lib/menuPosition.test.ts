import { describe, expect, it } from 'vitest'
import { placeMenu } from './menuPosition'

const viewport = { width: 1000, height: 800 }
const menu = { width: 200, height: 150 }
// A 30 px button whose right edge is at 900.
const button = (top: number) => ({ top, bottom: top + 30, left: 870, right: 900 })

describe('placeMenu', () => {
  it('opens below, aligned on the button', () => {
    expect(placeMenu(button(100), menu, viewport)).toEqual({ top: 134, left: 700 })
    expect(placeMenu(button(100), menu, viewport, 'left')).toEqual({ top: 134, left: 792 })
  })
  it('opens above when it does not fit below', () => {
    expect(placeMenu(button(700), menu, viewport)).toEqual({ top: 546, left: 700 })
  })
  it('stays inside the viewport when it fits neither below nor above', () => {
    const tall = { width: 200, height: 700 }
    expect(placeMenu(button(300), tall, viewport)).toEqual({ top: 92, left: 700 })
    expect(placeMenu(button(300), { width: 200, height: 900 }, viewport).top).toBe(8)
  })
  it('stays inside the viewport horizontally', () => {
    expect(placeMenu({ top: 100, bottom: 130, left: 10, right: 40 }, menu, viewport).left).toBe(8)
    expect(placeMenu({ top: 100, bottom: 130, left: 990, right: 1000 }, menu, viewport, 'left').left).toBe(792)
  })
})
