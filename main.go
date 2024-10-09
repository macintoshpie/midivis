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
	"sort"
	"strings"

	// "github.com/faiface/pixel"
	// "github.com/faiface/pixel/imdraw"
	// "github.com/faiface/pixel/pixelgl"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/audio"
	"github.com/hajimehoshi/ebiten/v2/audio/mp3"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"gitlab.com/gomidi/midi/v2/smf"
	"golang.org/x/image/colornames"
)

const width = 1024
const height = 768

// const midiFileName = "IDL main loop.mid"
const midiFileName = "sonatas_k-417_(c)sankey.mid"

// PPQN is the number of ticks per quarter note
// hardcoded for now, but we can get from midi header (division)
// IDL
const ppqn = 96

// Sonata
// const ppqn = 384

// Hardcoded for now, but we can get from midi as a tempo type event
// IDL
const microSecondsPerQuarterNote = 375000

// Sonata
// const microSecondsPerQuarterNote = 465000 // was originally 500000 but messed with it to get close to matching https://www.youtube.com/watch?v=OJ1p6hD-Df0

const debug = true

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
	bpm   int
	notes []Note
}

type Game struct {
	currentTick                int64
	tracks                     []*Track
	noteMin                    int
	noteHeight                 int
	noteTopBottomPaddingPixels int
	xScale                     float64
	xTranslate                 float64
}

func (g *Game) Update() error {
	g.currentTick++

	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	// convert screen render ticks (g.currentTick) to midi ticks
	// Each screen tick is assumed to be 1/60th of a second, probably need to fix this later
	elapsedDeltaTime := secondsToDeltaTime(float64(g.currentTick)*(1.0/60.0), microSecondsPerQuarterNote, ppqn)

	trackColors := []color.RGBA{
		colornames.White,
		colornames.Red,
		colornames.Orange,
		colornames.Yellow,
		colornames.Green,
		colornames.Blue,
		colornames.Indigo,
		colornames.Violet,
	}

	for index, t := range g.tracks {
		colorToUse := trackColors[index%len(trackColors)]

		if t.name == "./ag/introvocals.mid" {
			renderTrack2(t, screen, elapsedDeltaTime, g.noteMin, g.noteHeight, g.noteTopBottomPaddingPixels, g.xScale, g.xTranslate, colorToUse)
		} else {
			renderTrack(t, screen, elapsedDeltaTime, g.noteMin, g.noteHeight, g.noteTopBottomPaddingPixels, g.xScale, g.xTranslate, colorToUse)
		}
	}

	ebitenutil.DebugPrint(screen, fmt.Sprintf("ticks: %d\ndt: %d\nnheight: %d", g.currentTick, elapsedDeltaTime, g.noteHeight))
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
	}
}

func NewTrack(fileName string) *Track {

	return &Track{
		name:  fileName,
		notes: []Note{},
	}
}

func diy(logger *slog.Logger, fileName string) *Track {
	// Reference: https://midimusic.github.io/tech/midispec.html
	dat, err := os.Open(fileName)
	check(err)
	// logger.Info(string(dat))

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
	} else {
		// just panic for now
		panic("Division Type not supported")
		// division := binary.BigEndian.Uint16(dat[12:14])
		// logger.Info("Division (Ticks per Frame):", division)
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
	midiTrack := NewMidiTrack()
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

	track := NewTrack(fileName)
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

func renderTrack(t *Track, screen *ebiten.Image, elapsedDeltaTime int, noteMin int, noteHeight int, noteTopBottomPaddingPixels int, xScale float64, xTranslate float64, foregroundColor color.RGBA) {
	for _, note := range t.notes {
		noteY := noteHeight * (note.num - noteMin)
		// flip b/c we draw from upper left corner
		noteY = height - noteY

		isBeingPlayed := note.on <= elapsedDeltaTime && elapsedDeltaTime <= note.off

		noteX := float32(note.on-elapsedDeltaTime)*float32(xScale) + float32(xTranslate)
		noteWidth := float32(note.off-note.on) * float32(xScale)
		if isBeingPlayed {
			vector.DrawFilledRect(screen, noteX, float32(noteY), noteWidth, float32(noteHeight), foregroundColor, true)
		} else {
			strokeWidth := float32(1)
			vector.StrokeRect(screen, noteX, float32(noteY), noteWidth, float32(noteHeight), strokeWidth, colornames.White, true)
		}
	}
}

func renderTrack2(t *Track, screen *ebiten.Image, elapsedDeltaTime int, noteMin int, noteHeight int, noteTopBottomPaddingPixels int, xScale float64, xTranslate float64, foregroundColor color.RGBA) {
	for _, note := range t.notes {
		isBeingPlayed := note.on <= elapsedDeltaTime && elapsedDeltaTime <= note.off
		if !isBeingPlayed {
			continue
		}

		screen.Fill(colornames.Green)

		// timePlayed := elapsedDeltaTime - note.on
		// vector.DrawFilledRect(screen, noteX, float32(noteY), noteWidth, float32(noteHeight), foregroundColor, true)

	}
}
func startRender(tracks []*Track, logger *slog.Logger) {

	const oscillateColors = false

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

	// Use xScale to adjust the horizontal scaling of the notes
	const xScale = 0.5

	// Use xTranslate to adjust the horizontal translation of the notes (e.g. where the note-on should be occur)
	const xTranslate = width / 2

	// Setup audio player
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

	p.Play()

	// start tests
	// Playing with layering of tracks, we should probably turn tracks into a list of notes that point to a track renderer
	// so we can render on per-note basis
	sort.Slice(tracks, func(i, j int) bool {
		return tracks[i].name == "./ag/introvocals.mid"
	})
	// end tests

	ebiten.SetWindowSize(width, height)
	ebiten.SetWindowTitle("Hello, World!")
	if err := ebiten.RunGame(&Game{
		tracks:                     tracks,
		noteMin:                    noteMin,
		noteHeight:                 noteHeight,
		noteTopBottomPaddingPixels: noteTopBottomPaddingPixels,
		xScale:                     xScale,
		xTranslate:                 xTranslate,
	}); err != nil {
		log.Fatal(err)
	}
}

func pkg() {
	reader, err := smf.ReadFile(midiFileName)
	if err != nil {
		panic(err)
	}

	fmt.Println("Number of tracks:", reader.NumTracks())
	for index, track := range reader.Tracks {
		fmt.Println("Track", index)
		fmt.Println("Number of events:", len(track))
		for _, ev := range track {
			fmt.Println(ev.Delta, ev)
		}
	}
}

func main() {
	loggerLevel := slog.LevelInfo
	if debug {
		loggerLevel = slog.LevelDebug
	}
	loggerOpts := &slog.HandlerOptions{Level: loggerLevel}
	logger := slog.New(slog.NewTextHandler(os.Stdout, loggerOpts))

	argsWithoutProg := os.Args[1:]
	if len(argsWithoutProg) == 0 || argsWithoutProg[0] == "pkg" {
		pkg()
	} else if argsWithoutProg[0] == "diy" {
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
			track := diy(logger, filePath)
			tracks = append(tracks, track)
		}

		startRender(tracks, logger)
	} else {
		fmt.Println("Invalid command")
	}
}
