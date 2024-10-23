package main

import (
	"encoding/binary"
	"fmt"
	"image/color"
	"io"
	"log"
	"log/slog"
	"math"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	_ "embed"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/audio"
	"github.com/hajimehoshi/ebiten/v2/audio/mp3"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"golang.org/x/image/colornames"
)

//go:embed shaders/radialblur.kage
var radialblur_kage []byte

//go:embed shaders/colormod.kage
var colormod_kage []byte

//go:embed shaders/radialgradient.kage
var radialgradient_kage []byte

const width = 1024
const height = 768

// TODO: Hardcoded for now, but we can get from midi as a tempo type event
const microSecondsPerQuarterNote = 375000

const debug = false

type MidiNoteType byte

const (
	NoteOff MidiNoteType = 0x8
	NoteOn  MidiNoteType = 0x9
)

type MidiNote struct {
	deltaTime int
	eventType MidiNoteType
	channel   byte
	note      byte
	velocity  byte
}

type MidiTrack struct {
	notes []MidiNote
	// PPQN is the number of ticks per quarter note
	// It is pulled from midi header (division)
	ppqn uint16
}

type Note struct {
	on  int
	off int
	num int
	str string
	vel int
}

type Track struct {
	name  string
	ppqn  uint16
	bpm   int
	notes []Note
}

const (
	NoteTypeRect = iota
	NoteTypeScreen
	NoteTypeMeter
	NoteTypeZoom
	NoteTypeRadialGradient
)

var noteTypes = []int{
	NoteTypeRect,
	NoteTypeScreen,
	NoteTypeMeter,
	NoteTypeZoom,
	NoteTypeRadialGradient,
}

// Map midi files to animation types
var fileNameToType = map[string]int{
	"ah.mid":                NoteTypeRadialGradient,
	"bridgevocalbottom.mid": NoteTypeRect,
	"bridgevocaltop.mid":    NoteTypeRect,
	"cleanvocalsbottom.mid": NoteTypeRect,
	"click.mid":             NoteTypeRadialGradient,
	"cymbalend.mid":         NoteTypeMeter,
	"endbass.mid":           NoteTypeZoom,
	"flutevoice.mid":        NoteTypeZoom,
	"introvocals.mid":       NoteTypeRect,
	"kick.mid":              NoteTypeRadialGradient,
	"mainvocaltop.mid":      NoteTypeRect,
	"oohchords.mid":         NoteTypeZoom,
	"plucky.mid":            NoteTypeRect,
	"shew.mid":              NoteTypeZoom,
	"shimmery.mid":          NoteTypeRect,
	"shimmeryfast.mid":      NoteTypeRect,
	"shimmeryfastbass.mid":  NoteTypeZoom,
	"slidey.mid":            NoteTypeZoom,
}

type RenderableNoteBase struct {
	Note
	z int // z-index, used for rendering order
}

// NoteRect animates a rectangle across the screen during play
type NoteRect struct {
	RenderableNoteBase
	xScale float64
	color  *color.RGBA
}

// NoteScreen fills entire screen with color during play
type NoteScreen struct {
	RenderableNoteBase
	color *color.RGBA
}

// NoteMeter animates a rectangle from left to right during play filling screen
type NoteMeter struct {
	RenderableNoteBase
	color *color.RGBA
}

// NoteZoom animates a rectangle from center to full width during play
type NoteZoom struct {
	RenderableNoteBase
	color *color.RGBA
}

// NoteRadialGradient animates a radial gradient from center to full width during play
type NoteRadialGradient struct {
	RenderableNoteBase
	color *color.RGBA
}

type Renderable interface {
	GetZ() int
	Draw(screen *ebiten.Image, g *Game)
}

func (o *RenderableNoteBase) GetZ() int {
	return o.z
}

func (o *NoteRect) Draw(screen *ebiten.Image, g *Game) {

	// Draw the object
	noteY := g.noteHeight*(o.num-g.noteMin) + g.noteTopBottomPaddingPixels
	// flip b/c we draw from upper left corner
	noteY = height - noteY

	isBeingPlayed := o.on <= g.elapsedDeltaTime && g.elapsedDeltaTime <= o.off

	// set arbitrary velocity minimum and scale from there
	velMin := 100
	velRange := 127 - velMin
	xScaleVel := ((velMin - o.vel) / velRange) + 1
	noteX := float32(o.on-g.elapsedDeltaTime)*float32(xScaleVel) + float32(g.xTranslate)
	noteWidth := float32(o.off-o.on) * float32(xScaleVel)
	if isBeingPlayed {
		vector.DrawFilledRect(screen, noteX, float32(noteY), noteWidth, float32(g.noteHeight), o.color, true)

		// set the blur Y position to the note's Y position
		g.radialBlurShaderOpts.Uniforms["Center"] = []float32{float32(width) / 2.0, float32(noteY)}
	} else {
		strokeWidth := float32(1)
		vector.StrokeRect(screen, noteX, float32(noteY), noteWidth, float32(g.noteHeight), strokeWidth, o.color, true)
	}
}

func (o *NoteScreen) Draw(screen *ebiten.Image, g *Game) {
	// cover screen with color
	isBeingPlayed := o.on <= g.elapsedDeltaTime && g.elapsedDeltaTime <= o.off
	if isBeingPlayed {
		vector.DrawFilledRect(screen, 0, 0, float32(width), float32(height), o.color, true)
	}
}

func (o *NoteMeter) Draw(screen *ebiten.Image, g *Game) {
	// zoom in from small to large, filling up width of screen when being played
	deltaThreshold := g.ppqn

	// hasn't started
	if o.on-deltaThreshold > g.elapsedDeltaTime {
		return
	}

	// already finished
	if o.off < g.elapsedDeltaTime {
		return
	}

	isBeingPlayed := o.on <= g.elapsedDeltaTime && g.elapsedDeltaTime <= o.off
	if isBeingPlayed {
		noteX := float32(0)
		// noteY := o.num * g.noteHeight
		// Draw the object
		noteY := g.noteHeight*(o.num-g.noteMin) + g.noteTopBottomPaddingPixels
		// flip b/c we draw from upper left corner
		noteY = height - noteY

		pctUntilPlayStarts := float32(g.elapsedDeltaTime-o.on) / float32(deltaThreshold)
		// flip it
		pctUntilPlayStarts = 1 - pctUntilPlayStarts
		// width goes from 0 to width of screen
		noteWidth := width * pctUntilPlayStarts
		vector.DrawFilledRect(screen, noteX, float32(noteY), noteWidth, float32(g.noteHeight), o.color, true)
	}
}

func (o *NoteZoom) Draw(screen *ebiten.Image, g *Game) {
	// zoom in from small to large, filling up width of screen when being played
	deltaThreshold := g.ppqn * 2

	// hasn't started
	if o.on-deltaThreshold > g.elapsedDeltaTime {
		return
	}

	// already finished
	if o.off < g.elapsedDeltaTime {
		return
	}

	tUntilOn := max(float32(o.on-g.elapsedDeltaTime), 0.0)
	pctUntilPlayStarts := tUntilOn / float32(deltaThreshold)
	// flip it, so 0 is at beginning of threshold, 1 as at note on
	pctUntilPlayStarts = 1 - pctUntilPlayStarts

	// x is between 0 and width / 2
	noteX := float32(width) / 2 * pctUntilPlayStarts
	// flip x so it goes from width / 2 to 0
	noteX = float32(width)/2 - noteX
	distToMiddle := float32(width)/2 - noteX
	noteWidth := distToMiddle * 2

	noteY := g.noteHeight*(o.num-g.noteMin) + g.noteTopBottomPaddingPixels
	// flip b/c we draw from upper left corner
	noteY = height - noteY

	noteHeight := float32(g.noteHeight) * pctUntilPlayStarts

	isBeingPlayed := o.on <= g.elapsedDeltaTime && g.elapsedDeltaTime <= o.off
	if isBeingPlayed {
		vector.DrawFilledRect(screen, noteX, float32(noteY), noteWidth, noteHeight, o.color, true)
	} else {
		strokeWidth := float32(1)
		vector.StrokeRect(screen, noteX, float32(noteY), noteWidth, noteHeight, strokeWidth, o.color, true)
	}
}

func (o *NoteRadialGradient) Draw(screen *ebiten.Image, g *Game) {
	isBeingPlayed := o.on <= g.elapsedDeltaTime && g.elapsedDeltaTime <= o.off
	alreadyHandled := g.radialGradientShaderOpts.Uniforms["PctShow"] != 0

	if !isBeingPlayed || alreadyHandled {
		return
	}

	pctShow := float32(g.elapsedDeltaTime-o.on) / float32(o.off-o.on)
	g.radialGradientShaderOpts.Uniforms["PctShow"] = 1 - pctShow
	g.radialGradientShaderOpts.Uniforms["Color"] = []float32{float32(o.color.R), float32(o.color.G), float32(o.color.B), float32(o.color.A)}
}

type Game struct {
	currentTick                int64
	elapsedDeltaTime           int
	playerMeasure              int
	ppqn                       int
	tracks                     []*Track
	notes                      []Renderable
	noteMin                    int
	noteHeight                 int
	noteTopBottomPaddingPixels int
	xTranslate                 float64

	shader               *ebiten.Shader
	radialBlurShaderOpts *ebiten.DrawRectShaderOptions

	colormodShader *ebiten.Shader

	radialGradientShader     *ebiten.Shader
	radialGradientShaderOpts *ebiten.DrawRectShaderOptions

	playerPosition time.Duration
	player         *audio.Player
}

func (g *Game) Update() error {
	if g.player.IsPlaying() {
		g.playerPosition = g.player.Position()
		g.elapsedDeltaTime = secondsToDeltaTime(float64(g.playerPosition.Milliseconds())/1000.0, microSecondsPerQuarterNote, g.ppqn)
	} else {
		// If not playing, just use ticks to track time
		g.currentTick++
		// convert screen render ticks (g.currentTick) to midi ticks
		// Each screen tick is assumed to be 1/60th of a second, probably need to fix this later
		g.elapsedDeltaTime = secondsToDeltaTime(float64(g.currentTick)*(1.0/60.0), microSecondsPerQuarterNote, g.ppqn)

	}

	g.playerMeasure = g.elapsedDeltaTime / (g.ppqn * 4)

	// if right key just released, seek a bit
	if inpututil.IsKeyJustPressed(ebiten.KeyRight) {
		err := g.seekToMeasure(g.playerMeasure + 1)

		if err != nil {
			return err
		}
	}

	// Update shader uniforms
	g.radialGradientShaderOpts.Uniforms["PctShow"] = 0

	cx, cy := ebiten.CursorPosition()
	g.radialBlurShaderOpts.Uniforms["Time"] = float32(g.currentTick) / 60
	g.radialBlurShaderOpts.Uniforms["Cursor"] = []float32{float32(cx), float32(cy)}

	return nil
}

// seekToTime seeks to a specific time in the audio file
func (g *Game) seekToTime(t time.Duration) error {
	if err := g.player.SetPosition(t); err != nil {
		return err
	}

	return nil
}

// seekToMeasure seeks to a specific measure in the audio file
func (g *Game) seekToMeasure(m int) error {
	deltaTime := m * g.ppqn * 4
	t := deltaTimeToSeconds(deltaTime, microSecondsPerQuarterNote, g.ppqn)
	nanoSec := int64(t * 1000000000)
	if err := g.seekToTime(time.Duration(nanoSec)); err != nil {
		return err
	}

	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {

	baseImage := ebiten.NewImage(width, height)
	for _, note := range g.notes {
		note.Draw(baseImage, g)
	}

	blurImage := ebiten.NewImage(width, height)
	blurImage.DrawRectShader(width, height, g.shader, g.radialBlurShaderOpts)

	g.radialBlurShaderOpts.Images[0] = baseImage
	g.radialGradientShaderOpts.Images[0] = blurImage

	screen.DrawRectShader(width, height, g.radialGradientShader, g.radialGradientShaderOpts)

	measurePosition := g.elapsedDeltaTime / (g.ppqn * 4)
	if debug {
		ebitenutil.DebugPrint(screen, fmt.Sprintf("playerPosition: %d\nmeasurePosition: %d", g.playerPosition, measurePosition))
	}
}

func (g *Game) Layout(outsideWidth, outsideHeight int) (screenWidth, screenHeight int) {
	return width, height
}

func check(e error) {
	if e != nil {
		panic(e)
	}
}

func noteNumberToString(noteNumber byte) string {
	notes := []string{
		"C",
		"C#",
		"D",
		"D#",
		"E",
		"F",
		"F#",
		"G",
		"G#",
		"A",
		"A#",
		"B",
	}
	octave := int(noteNumber / 12)
	note := int(noteNumber % 12)
	return fmt.Sprintf("%s%d", notes[note], octave)
}

func readVariableLengthValue2(dat io.Reader) (result int) {
	result = 0
	for {
		b := make([]byte, 1)
		_, err := dat.Read(b)
		check(err)
		result = (result << 7) | int(b[0]&0x7F)
		if b[0]&0x80 == 0 {
			break
		}
	}

	return result
}

func NewMidiTrack() *MidiTrack {

	return &MidiTrack{
		notes: []MidiNote{},
		ppqn:  0,
	}
}

func NewTrack(fileName string, ppqn uint16) *Track {

	return &Track{
		name:  path.Base(fileName),
		notes: []Note{},
		ppqn:  ppqn,
	}
}

func parseMidiFile(logger *slog.Logger, fileName string) *MidiTrack {
	// Reference: https://midimusic.github.io/tech/midispec.html
	dat, err := os.Open(fileName)
	check(err)
	defer dat.Close()
	midiTrack := NewMidiTrack()

	// first 4 bytes (32 bits) are the header type in ascii
	headerBytes := make([]byte, 4)
	_, err = dat.Read(headerBytes)
	check(err)
	logger.Info("Header Type:", string(headerBytes))

	// length is the next 4 bytes (32 bits) in big endian
	lengthBytes := make([]byte, 4)
	_, err = dat.Read(lengthBytes)
	lengthInt := binary.BigEndian.Uint32(lengthBytes)
	logger.Info("Length:", lengthInt)

	// -- Data Section --
	// format is the next 2 bytes (16 bits) in big endian
	formatBytes := make([]byte, 2)
	_, err = dat.Read(formatBytes)
	formatInt := binary.BigEndian.Uint16(formatBytes)
	logger.Info("Format:", formatInt)
	if formatInt != 0 {
		panic("Format not supported")
	}

	// ntracks is the next 2 bytes (16 bits) in big endian
	nTracksBytes := make([]byte, 2)
	_, err = dat.Read(nTracksBytes)
	nTracksInt := binary.BigEndian.Uint16(nTracksBytes)
	logger.Info("NTracks:", nTracksInt)

	// division is the next 2 bytes (16 bits) in big endian
	// if the first bit is 0, the remaining 15 bits represent the number of ticks quarter note
	//   For instance, if division is 96, then a time interval of an eighth-note between two events in the file would be 48
	// if the first bit is 1, the remaining 15 bits represent the number of ticks per frame
	divisionTypeBytes := make([]byte, 2)
	_, err = dat.Read(divisionTypeBytes)
	logger.Info("Division Type:", divisionTypeBytes[0])

	if divisionTypeBytes[0]&0x80 == 0 {
		division := binary.BigEndian.Uint16(divisionTypeBytes)
		logger.Info("Division (Ticks per Quarter Note):", division)
		midiTrack.ppqn = division
	} else {
		// just panic for now
		panic("Division Type not supported")
	}

	// -- Track Section --
	// The format for Track Chunks (described below) is exactly the same for all three formats (0, 1, and 2: see "Header Chunk" above) of MIDI Files.
	// <Track Chunk> = <chunk type><length><MTrk event>+
	// track header is the next 4 bytes (32 bits) in ascii
	trackHeaderBytes := make([]byte, 4)
	_, err = dat.Read(trackHeaderBytes)
	logger.Info("Track Header:", string(trackHeaderBytes))

	// track length is the next 4 bytes (32 bits) in big endian
	trackLengthBytes := make([]byte, 4)
	_, err = dat.Read(trackLengthBytes)
	trackLengthInt := binary.BigEndian.Uint32(trackLengthBytes)
	logger.Info("Track Length:", trackLengthInt)

	// read track data in the format:
	// <MTrk event> = <delta-time><event>
	// <delta-time> is stored as a variable-length quantity.
	// It represents the amount of time before the following event.
	// 	If the first event in a track occurs at the very beginning of a track, or if two events occur simultaneously, a delta-time of zero is used. Delta-times are always present.
	// (Not storing delta-times of 0 requires at least two bytes for any other value, and most delta-times aren't zero.)
	// Delta-time is in some fraction of a beat (or a second, for recording a track with SMPTE times), as specified in the header chunk.
	// <event> = <MIDI event> | <sysex event> | <meta-event>
	// Print only note on and note offf midi events and their data as well as delta time events
	// eventsRemaining := 6
	done := false
	for !done {
		// eventsRemaining--
		logger.Debug("------- EVENT -------")
		deltaTime := readVariableLengthValue2(dat)
		logger.Debug("Delta Time:", deltaTime)

		// <event> = <MIDI event> | <sysex event> | <meta-event>
		eventFirstByte := make([]byte, 1)
		_, err = dat.Read(eventFirstByte)
		check(err)
		logger.Debug("Event first byte: %x\n", eventFirstByte[0])

		if eventFirstByte[0] == 0xFF {
			// <meta-event> = 0xFF<type><length><data>
			metaEventType := make([]byte, 1)
			_, err = dat.Read(metaEventType)
			check(err)

			metaEventLength := readVariableLengthValue2(dat)

			switch metaEventType[0] {
			case 0x03:
				{
					trackName := make([]byte, metaEventLength)
					_, err = dat.Read(trackName)
					check(err)
					logger.Debug("Meta Event Type: %s (Track Name)\n", trackName)
					logger.Debug("  Track Name:", string(trackName))

					break
				}
			case 0x2F:
				{
					logger.Debug("Meta Event Type: %x (End of Track)\n", metaEventType[0])
					if metaEventLength != 0 {
						panic("Invalid End of Track Length")
					}
					// consume the data even though we don't use it now
					// metaEventData := make([]byte, metaEventLength)
					// _, err = dat.Read(metaEventData)
					// check(err)
					done = true
					break
				}
			case 0x58:
				{
					logger.Debug("Meta Event Type: %x (Time Signature)\n", metaEventType[0])
					if metaEventLength != 4 {
						panic("Invalid Time Signature Length")
					}

					numerator := make([]byte, 1)
					_, err = dat.Read(numerator)
					check(err)
					denominator := make([]byte, 1)
					_, err = dat.Read(denominator)
					check(err)
					cc := make([]byte, 1)
					_, err = dat.Read(cc)
					check(err)
					bb := make([]byte, 1)
					_, err = dat.Read(bb)
					check(err)
					logger.Debug("  Numerator:", numerator[0])
					logger.Debug("  Denominator:", denominator[0])
					break
				}
			case 0x51:
				{
					logger.Debug("Meta Event Type: %x (Set Tempo)\n", metaEventType[0])
					if metaEventLength != 3 {
						panic("Invalid Set Tempo Length")
					}

					mpqn := make([]byte, 3)
					_, err = dat.Read(mpqn)
					check(err)
					microSecondsPerQuarterNoteInt := uint32(mpqn[0])<<16 | uint32(mpqn[1])<<8 | uint32(mpqn[2])
					logger.Info("  Microseconds Per Quarter Note:", microSecondsPerQuarterNoteInt)
					break
				}
			default:
				logger.Debug("Meta Event Type: %x\n", metaEventType[0])
				logger.Debug("Meta Event Length:", metaEventLength)

				// consume the data even though we don't use it now
				metaEventData := make([]byte, metaEventLength)
				_, err = dat.Read(metaEventData)
				check(err)
			}

			// logger.Debug("Meta Event Data:", string(metaEventData))
		} else if eventFirstByte[0] == 0xF0 || eventFirstByte[0] == 0xF7 {
			// <sysex event> = 0xF0<length><data> or 0xF7<length><data>
			sysexEventLength := readVariableLengthValue2(dat)
			logger.Debug("Sysex Event Length:", sysexEventLength)
			// consume the data even though we don't use it now
			sysexEventData := make([]byte, sysexEventLength)
			_, err = dat.Read(sysexEventData)
			check(err)
		} else {
			// <MIDI event> = <MIDI event type><channel><data>
			// <MIDI event type> = <MIDI event type (4 bits)><MIDI channel (4 bits)>
			// <MIDI event type> = 0x8 for note off, 0x9 for note on
			midiEventType := eventFirstByte[0]
			logger.Debug("RAW MIDI Event Type: %x\n", midiEventType)

			// midiChannel := midiEventType & 0x0F
			midiEventType = midiEventType >> 4

			switch midiEventType {
			case 0x8:
				{
					logger.Debug("MIDI Event Type: Note Off")
					note := make([]byte, 1)
					_, err = dat.Read(note)
					check(err)
					velocity := make([]byte, 1)
					_, err = dat.Read(velocity)
					check(err)
					logger.Debug("  Note:", note[0], noteNumberToString(note[0]))
					logger.Debug("  Velocity:", velocity[0])

					midiTrack.notes = append(midiTrack.notes, MidiNote{
						deltaTime: deltaTime,
						eventType: NoteOff,
						channel:   0,
						note:      note[0],
						velocity:  velocity[0],
					})
					break
				}
			case 0x9:
				{
					logger.Debug("MIDI Event Type: Note On")
					note := make([]byte, 1)
					_, err = dat.Read(note)
					check(err)
					velocity := make([]byte, 1)
					_, err = dat.Read(velocity)
					check(err)
					logger.Debug("  Note:", note[0], noteNumberToString(note[0]))
					logger.Debug("  Velocity:", velocity[0])

					midiTrack.notes = append(midiTrack.notes, MidiNote{
						deltaTime: deltaTime,
						eventType: NoteOn,
						channel:   0,
						note:      note[0],
						velocity:  velocity[0],
					})
					break
				}
			}
		}
	}

	return midiTrack
}

func (midiTrack *MidiTrack) ToTrack(logger *slog.Logger, fileName string) *Track {
	track := NewTrack(fileName, midiTrack.ppqn)
	deltaTotal := 0
	noteOnMap := make(map[byte]Note)
	for _, midiNote := range midiTrack.notes {
		deltaTotal += midiNote.deltaTime

		if midiNote.eventType == NoteOn {
			noteOnMap[midiNote.note] = Note{
				on:  deltaTotal,
				off: -1,
				num: int(midiNote.note),
				str: noteNumberToString(midiNote.note),
				vel: int(midiNote.velocity),
			}
		} else if midiNote.eventType == NoteOff {
			if foundNote, ok := noteOnMap[midiNote.note]; ok {
				foundNote.off = deltaTotal
				track.notes = append(track.notes, foundNote)
				delete(noteOnMap, midiNote.note)
			} else {
				logger.Info("Note Off without Note On")
			}
		}
	}

	return track
}

func secondsToDeltaTime(elapsedTime float64, microSecondsPerQuarterNote int, ppqn int) int {
	// Convert microseconds per quarter note to seconds per tick
	secondsPerTick := float64(microSecondsPerQuarterNote) / (1000000.0 * float64(ppqn))

	// Calculate delta time in ticks
	deltaTime := elapsedTime / secondsPerTick

	// Round to the nearest integer (since delta time must be an integer value in MIDI)
	return int(math.Round(deltaTime))
}

func deltaTimeToSeconds(deltaTime int, microSecondsPerQuarterNote int, ppqn int) float64 {
	// Convert microseconds per quarter note to seconds per tick
	secondsPerTick := float64(microSecondsPerQuarterNote) / (1000000.0 * float64(ppqn))

	// Calculate elapsed time in seconds
	elapsedTime := float64(deltaTime) * secondsPerTick

	return elapsedTime
}

// startRender starts the rendering loop
func startRender(tracks []*Track, logger *slog.Logger) {
	// Use noteTopBottomPaddingPixels to adjust the padding at the top and bottom of screen for notes
	const noteTopBottomPaddingPixels = 50

	// Use Normalize and/or noteMin/noteMax to adjust the range of notes displayed
	const normalize = true
	noteMin := 0
	noteMax := 127
	if normalize {
		allNotes := make([]Note, 0)
		for _, t := range tracks {
			allNotes = append(allNotes, t.notes...)
		}

		sort.Slice(allNotes, func(i, j int) bool {
			return allNotes[i].num < allNotes[j].num
		})

		logger.Debug("Sorted Notes:")
		for _, note := range allNotes {
			logger.Debug("Note: %d %d %d %d\n", note.num, note.on, note.off, note.vel)
		}

		noteMin = allNotes[0].num
		noteMax = allNotes[len(allNotes)-1].num
	}

	noteHeight := (height - noteTopBottomPaddingPixels*2) / (noteMax - noteMin)

	// Use xTranslate to adjust the horizontal translation of the notes (e.g. where the note-on should be occur)
	const xTranslate = width / 2

	// Setup audio player
	// TODO: hardcoded for now...
	const sampleRate = 44100
	audioContext := audio.NewContext(sampleRate)

	audioFile, err := os.Open("A. G. Cook - Idyll.mp3")
	check(err)
	s, err := mp3.DecodeF32(audioFile)
	if err != nil {
		panic(err)
	}

	p, err := audioContext.NewPlayerF32(s)
	if err != nil {
		panic(err)
	}

	ebiten.SetWindowSize(width, height)
	ebiten.SetWindowTitle("Hello, World!")
	notes := make([]Renderable, 0)
	for trackIndex, t := range tracks {
		typeToUse, ok := fileNameToType[t.name]
		if !ok {
			logger.Info("Using default note type", "trackName", t.name)
			typeToUse = NoteTypeRect
		}
		colorsToUse := []color.RGBA{
			colornames.Red,
			colornames.Blue,
			colornames.Green,
			colornames.Yellow,
			colornames.Purple,
			colornames.White,
		}
		chosenColor := colorsToUse[trackIndex%len(colorsToUse)]
		for noteIndex, note := range t.notes {
			if typeToUse == NoteTypeScreen {
				z := -10
				notes = append(notes, &NoteScreen{
					RenderableNoteBase: RenderableNoteBase{
						Note: note,
						z:    z,
					},
					color: &chosenColor,
				})
			} else if typeToUse == NoteTypeMeter {
				z := -5
				notes = append(notes, &NoteMeter{
					RenderableNoteBase: RenderableNoteBase{
						Note: note,
						z:    z,
					},
					color: &chosenColor,
				})
			} else if typeToUse == NoteTypeZoom {
				z := -1
				notes = append(notes, &NoteZoom{
					RenderableNoteBase: RenderableNoteBase{
						Note: note,
						z:    z,
					},
					color: &chosenColor,
				})
			} else if typeToUse == NoteTypeRadialGradient {
				z := 0
				notes = append(notes, &NoteRadialGradient{
					RenderableNoteBase: RenderableNoteBase{
						Note: note,
						z:    z,
					},
					color: &chosenColor,
				})
			} else {
				z := 0
				xScale := 2.0
				if noteIndex%2 == 0 {
					xScale = 1
				}
				notes = append(notes, &NoteRect{
					RenderableNoteBase: RenderableNoteBase{
						Note: note,
						z:    z,
					},
					color:  &chosenColor,
					xScale: xScale,
				})
			}
		}

		// kind of dumb to sort here but let's do it anyways for now
		sort.Slice(notes, func(i, j int) bool {
			return notes[i].GetZ() < notes[j].GetZ()
		})
	}

	shader, err := ebiten.NewShader(radialblur_kage)
	if err != nil {
		log.Fatal(err)
	}
	radialBlurShaderOpts := &ebiten.DrawRectShaderOptions{}
	radialBlurShaderOpts.Uniforms = map[string]any{
		"Time":   0,
		"Cursor": []float32{float32(0), float32(0)},
		"Center": []float32{float32(width / 2), float32(height / 2)},
	}

	colormodShader, err := ebiten.NewShader(colormod_kage)
	if err != nil {
		log.Fatal(err)
	}

	radialGradientShader, err := ebiten.NewShader(radialgradient_kage)
	if err != nil {
		log.Fatal(err)
	}

	radialGradientShaderOpts := &ebiten.DrawRectShaderOptions{}
	radialGradientShaderOpts.Uniforms = map[string]interface{}{
		"PctShow": 0,
	}

	p.Play()

	game := &Game{
		currentTick:      0,
		elapsedDeltaTime: 0,
		playerMeasure:    0,
		// Assuming all tracks are the same ppqn...
		ppqn:                       int(tracks[0].ppqn),
		tracks:                     tracks,
		notes:                      notes,
		noteMin:                    noteMin,
		noteHeight:                 noteHeight,
		noteTopBottomPaddingPixels: noteTopBottomPaddingPixels,
		xTranslate:                 xTranslate,

		shader:               shader,
		radialBlurShaderOpts: radialBlurShaderOpts,

		colormodShader: colormodShader,

		radialGradientShader:     radialGradientShader,
		radialGradientShaderOpts: radialGradientShaderOpts,

		player: p,
	}

	if err := ebiten.RunGame(game); err != nil {
		log.Fatal(err)
	}
}

func main() {
	loggerLevel := slog.LevelInfo
	if debug {
		loggerLevel = slog.LevelDebug
	}
	loggerOpts := &slog.HandlerOptions{Level: loggerLevel}
	logger := slog.New(slog.NewTextHandler(os.Stdout, loggerOpts))

	tracks := make([]*Track, 0)

	files, err := os.ReadDir("./ag")
	if err != nil {
		panic(err)
	}

	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".mid") {
			continue
		}

		filePath := fmt.Sprintf("./ag/%s", file.Name())
		midiTrack := parseMidiFile(logger, filePath)
		tracks = append(tracks, midiTrack.ToTrack(logger, file.Name()))
	}

	startRender(tracks, logger)
}
