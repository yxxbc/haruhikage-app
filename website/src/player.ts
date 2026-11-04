import { createSignal } from 'solid-js'

// The song the window in the hero plays. It ships no audio: the song is
// theirs, and the demo shows the player, not the music.
export const track = {
  title: '春日影',
  artist: 'MyGO!!!!!',
  duration: 281,
}

const [playing, setPlaying] = createSignal(false)
const [position, setPosition] = createSignal(62)
const [liked, setLiked] = createSignal(false)
export { playing, position, liked, setLiked }

// The position the demo plays from, counted on by the clock rather than by
// a player: the progress bar, the cover and the "playing" state all follow
// it, so the window still shows what playing looks like.
let frame = 0
let last = 0

function tick(now: number) {
  if (last === 0) last = now
  const seconds = (now - last) / 1000
  last = now
  setPosition((p) => {
    const next = p + seconds
    // Like the track, the demo starts over at its end.
    return next >= track.duration ? next % track.duration : next
  })
  frame = requestAnimationFrame(tick)
}

export function play() {
  if (playing()) return
  setPlaying(true)
  cancelAnimationFrame(frame)
  last = 0
  frame = requestAnimationFrame(tick)
}

export function pause() {
  setPlaying(false)
  cancelAnimationFrame(frame)
}

export function toggle() {
  playing() ? pause() : play()
}

export function seek(fraction: number) {
  const p = Math.max(0, Math.min(1, fraction)) * track.duration
  setPosition(p)
}

export function clock(seconds: number) {
  const s = Math.floor(seconds)
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}
