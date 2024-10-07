package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/faiface/pixel"
	"github.com/faiface/pixel/imdraw"
	"github.com/faiface/pixel/pixelgl"
	"gitlab.com/gomidi/midi/v2/smf"
	"golang.org/x/image/colornames"
)

const midiFileName = "IDL main loop.mid"

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
	bpm   int
	notes []Note
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

func NewTrack() *Track {

	return &Track{
		notes: []Note{},
	}
}

func diy() *Track {
	// Reference: https://midimusic.github.io/tech/midispec.html
	dat, err := os.Open(midiFileName)
	check(err)
	// fmt.Println(string(dat))

	// first 4 bytes (32 bits) are the header type in ascii
	headerBytes := make([]byte, 4)
	_, err = dat.Read(headerBytes)
	check(err)
	fmt.Println("Header Type:", string(headerBytes))

	// length is the next 4 bytes (32 bits) in big endian
	lengthBytes := make([]byte, 4)
	_, err = dat.Read(lengthBytes)
	lengthInt := binary.BigEndian.Uint32(lengthBytes)
	fmt.Println("Length:", lengthInt)

	// -- Data Section --
	// format is the next 2 bytes (16 bits) in big endian
	formatBytes := make([]byte, 2)
	_, err = dat.Read(formatBytes)
	formatInt := binary.BigEndian.Uint16(formatBytes)
	fmt.Println("Format:", formatInt)
	if formatInt != 0 {
		panic("Format not supported")
	}

	// ntracks is the next 2 bytes (16 bits) in big endian
	nTracksBytes := make([]byte, 2)
	_, err = dat.Read(nTracksBytes)
	nTracksInt := binary.BigEndian.Uint16(nTracksBytes)
	fmt.Println("NTracks:", nTracksInt)

	// division is the next 2 bytes (16 bits) in big endian
	// if the first bit is 0, the remaining 15 bits represent the number of ticks quarter note
	//   For instance, if division is 96, then a time interval of an eighth-note between two events in the file would be 48
	// if the first bit is 1, the remaining 15 bits represent the number of ticks per frame
	divisionTypeBytes := make([]byte, 2)
	_, err = dat.Read(divisionTypeBytes)
	fmt.Println("Division Type:", divisionTypeBytes[0])

	if divisionTypeBytes[0]&0x80 == 0 {
		division := binary.BigEndian.Uint16(divisionTypeBytes)
		fmt.Println("Division (Ticks per Quarter Note):", division)
	} else {
		// just panic for now
		panic("Division Type not supported")
		// division := binary.BigEndian.Uint16(dat[12:14])
		// fmt.Println("Division (Ticks per Frame):", division)
	}

	// -- Track Section --
	// The format for Track Chunks (described below) is exactly the same for all three formats (0, 1, and 2: see "Header Chunk" above) of MIDI Files.
	// <Track Chunk> = <chunk type><length><MTrk event>+
	// track header is the next 4 bytes (32 bits) in ascii
	trackHeaderBytes := make([]byte, 4)
	_, err = dat.Read(trackHeaderBytes)
	fmt.Println("Track Header:", string(trackHeaderBytes))

	// track length is the next 4 bytes (32 bits) in big endian
	trackLengthBytes := make([]byte, 4)
	_, err = dat.Read(trackLengthBytes)
	trackLengthInt := binary.BigEndian.Uint32(trackLengthBytes)
	fmt.Println("Track Length:", trackLengthInt)

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
		fmt.Println("------- EVENT -------")
		deltaTime := readVariableLengthValue2(dat)
		fmt.Println("Delta Time:", deltaTime)

		// <event> = <MIDI event> | <sysex event> | <meta-event>
		eventFirstByte := make([]byte, 1)
		_, err = dat.Read(eventFirstByte)
		check(err)
		fmt.Printf("Event first byte: %x\n", eventFirstByte[0])

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
					fmt.Printf("Meta Event Type: %s (Track Name)\n", trackName)
					fmt.Println("  Track Name:", string(trackName))

					break
				}
			case 0x2F:
				{
					fmt.Printf("Meta Event Type: %x (End of Track)\n", metaEventType[0])
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
					fmt.Printf("Meta Event Type: %x (Time Signature)\n", metaEventType[0])
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
					fmt.Println("  Numerator:", numerator[0])
					fmt.Println("  Denominator:", denominator[0])
					break
				}
			case 0x51:
				{
					fmt.Printf("Meta Event Type: %x (Set Tempo)\n", metaEventType[0])
					if metaEventLength != 3 {
						panic("Invalid Set Tempo Length")
					}

					mpqn := make([]byte, 3)
					_, err = dat.Read(mpqn)
					check(err)
					microSecondsPerQuarterNoteInt := uint32(mpqn[0])<<16 | uint32(mpqn[1])<<8 | uint32(mpqn[2])
					fmt.Println("  Microseconds Per Quarter Note:", microSecondsPerQuarterNoteInt)
					break
				}
			default:
				fmt.Printf("Meta Event Type: %x\n", metaEventType[0])
				fmt.Println("Meta Event Length:", metaEventLength)

				// consume the data even though we don't use it now
				metaEventData := make([]byte, metaEventLength)
				_, err = dat.Read(metaEventData)
				check(err)
			}

			// fmt.Println("Meta Event Data:", string(metaEventData))
		} else if eventFirstByte[0] == 0xF0 || eventFirstByte[0] == 0xF7 {
			// <sysex event> = 0xF0<length><data> or 0xF7<length><data>
			sysexEventLength := readVariableLengthValue2(dat)
			fmt.Println("Sysex Event Length:", sysexEventLength)
			// consume the data even though we don't use it now
			sysexEventData := make([]byte, sysexEventLength)
			_, err = dat.Read(sysexEventData)
			check(err)
		} else {
			// <MIDI event> = <MIDI event type><channel><data>
			// <MIDI event type> = <MIDI event type (4 bits)><MIDI channel (4 bits)>
			// <MIDI event type> = 0x8 for note off, 0x9 for note on
			midiEventType := eventFirstByte[0]
			fmt.Printf("RAW MIDI Event Type: %x\n", midiEventType)

			// midiChannel := midiEventType & 0x0F
			midiEventType = midiEventType >> 4

			switch midiEventType {
			case 0x8:
				{
					fmt.Println("MIDI Event Type: Note Off")
					note := make([]byte, 1)
					_, err = dat.Read(note)
					check(err)
					velocity := make([]byte, 1)
					_, err = dat.Read(velocity)
					check(err)
					fmt.Println("  Note:", note[0], noteNumberToString(note[0]))
					fmt.Println("  Velocity:", velocity[0])

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
					fmt.Println("MIDI Event Type: Note On")
					note := make([]byte, 1)
					_, err = dat.Read(note)
					check(err)
					velocity := make([]byte, 1)
					_, err = dat.Read(velocity)
					check(err)
					fmt.Println("  Note:", note[0], noteNumberToString(note[0]))
					fmt.Println("  Velocity:", velocity[0])

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

	track := NewTrack()
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
				fmt.Println("Note Off without Note On")
			}
		}
	}

	// for _, note := range track.notes {
	// 	fmt.Printf("Note: %s %d %d %d\n", note.str, note.on, note.off, note.vel)
	// }

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

func render(t *Track) {
	const width = 1024
	const height = 768
	const noteHeight = height / 128

	// PPQN is the number of ticks per quarter note
	// hardcoded for now, but we can get from midi header (division)
	const ppqn = 480
	// Hardcoded for now, but we can get from midi as a tempo type event
	const microSecondsPerQuarterNote = 375000

	const xScale = 0.25
	const xTranslate = width / 2
	run := func() {
		cfg := pixelgl.WindowConfig{
			Title:  "Ted is v cool!",
			Bounds: pixel.R(0, 0, width, height),
			VSync:  true,
		}
		win, err := pixelgl.NewWindow(cfg)
		if err != nil {
			panic(err)
		}

		imd := imdraw.New(nil)

		fps30 := time.Tick(time.Second / 30)
		start := time.Now()
		for !win.Closed() {
			win.Clear(colornames.Violet)
			imd.Clear()
			imd.Reset()

			if win.Pressed(pixelgl.KeySpace) {
				start = time.Now()
			}

			// draw some lines for notes
			// for i := 0; i < 128; i++ {
			// 	imd.Color = colornames.Black
			// 	imd.Push(pixel.V(0, float64(i*noteHeight)))
			// 	imd.Push(pixel.V(width, float64(i*noteHeight)))
			// 	imd.Line(1)
			// }

			elapsedSeconds := time.Since(start).Seconds()
			elapsedDeltaTime := secondsToDeltaTime(elapsedSeconds, microSecondsPerQuarterNote, ppqn)

			for _, note := range t.notes {
				// fmt.Printf("Note: %d %d %d %d\n", note.num, note.on, note.off, note.vel)

				noteY := noteHeight * note.num
				isBeingPlayed := note.on <= elapsedDeltaTime && elapsedDeltaTime <= note.off
				if isBeingPlayed {
					// fractionRemaining := float64(note.off-elapsedDeltaTime) / float64(note.off-note.on)

					imd.Color = colornames.White
				} else {
					imd.Color = colornames.Black
				}
				// bottom left point
				imd.Push(pixel.V(float64((note.on-elapsedDeltaTime))*xScale+xTranslate, float64(noteY)))
				// top right point
				imd.Push(pixel.V(float64((note.off-elapsedDeltaTime))*xScale+xTranslate, float64(noteY+noteHeight)))
				if isBeingPlayed {
					imd.Rectangle(0)
				} else {
					imd.Rectangle(2)
				}
			}

			// vertical line in center
			imd.Color = colornames.White
			imd.Push(pixel.V(width/2, height), pixel.V(width/2, 0))
			imd.Line(2)

			imd.Draw(win)
			win.Update()
			<-fps30
		}
	}

	pixelgl.Run(run)
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
	argsWithoutProg := os.Args[1:]
	if len(argsWithoutProg) == 0 || argsWithoutProg[0] == "pkg" {
		pkg()
	} else if argsWithoutProg[0] == "diy" {
		track := diy()
		render(track)
	} else {
		fmt.Println("Invalid command")
	}
}
