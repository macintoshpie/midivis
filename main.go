package main

import (
	"encoding/binary"
	"fmt"
	"image/color"
	"io"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ebitengine/oto/v3"
	"github.com/faiface/pixel"
	"github.com/faiface/pixel/imdraw"
	"github.com/faiface/pixel/pixelgl"
	"github.com/hajimehoshi/go-mp3"
	"gitlab.com/gomidi/midi/v2/smf"
	"golang.org/x/image/colornames"
)

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

func renderTrack(t *Track, imd *imdraw.IMDraw, elapsedDeltaTime int, noteMin int, noteHeight int, noteTopBottomPaddingPixels int, xScale float64, xTranslate float64, foregroundColor color.RGBA) {
	for _, note := range t.notes {
		noteY := noteHeight*(note.num-noteMin) + noteTopBottomPaddingPixels
		isBeingPlayed := note.on <= elapsedDeltaTime && elapsedDeltaTime <= note.off

		// draw a circle behind it if it's being played
		// if isBeingPlayed {
		// 	fractionRemaining := float64(note.off-elapsedDeltaTime) / float64(note.off-note.on)
		// 	// semiTransparent := pixel.RGB(0, 0, 0).Mul(pixel.Alpha(fractionRemaining))
		// 	imd.Color = pixel.RGB(float64(accentColor.R), float64(accentColor.G), float64(accentColor.B)).Mul(pixel.Alpha(fractionRemaining - 0.2))
		// 	imd.Push(pixel.V(width/2, float64(noteY)))
		// 	imd.Circle(50, 0)
		// }

		if isBeingPlayed {
			// fractionRemaining := float64(note.off-elapsedDeltaTime) / float64(note.off-note.on)

			imd.Color = foregroundColor
		} else {
			imd.Color = foregroundColor
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
}

func renderAll(tracks []*Track, logger *slog.Logger) {
	const width = 1024
	const height = 768

	const oscillateColors = false

	// Use noteTopBottomPaddingPixels to adjust the padding at the top and bottom of screen for notes
	const noteTopBottomPaddingPixels = 100

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

	// Audio setup
	file, err := os.Open("A. G. Cook - Idyll.mp3")
	if err != nil {
		panic("opening my-file.mp3 failed: " + err.Error())
	}

	// Decode file. This process is done as the file plays so it won't
	// load the whole thing into memory.
	decodedMp3, err := mp3.NewDecoder(file)
	if err != nil {
		panic("mp3.NewDecoder failed: " + err.Error())
	}

	// Prepare an Oto context (this will use your default audio device) that will
	// play all our sounds. Its configuration can't be changed later.

	op := &oto.NewContextOptions{}

	// Usually 44100 or 48000. Other values might cause distortions in Oto
	op.SampleRate = 44100

	// Number of channels (aka locations) to play sounds from. Either 1 or 2.
	// 1 is mono sound, and 2 is stereo (most speakers are stereo).
	op.ChannelCount = 2

	// Format of the source. go-mp3's format is signed 16bit integers.
	op.Format = oto.FormatSignedInt16LE

	// Remember that you should **not** create more than one context
	otoCtx, readyChan, err := oto.NewContext(op)
	if err != nil {
		panic("oto.NewContext failed: " + err.Error())
	}
	// It might take a bit for the hardware audio devices to be ready, so we wait on the channel.
	<-readyChan

	// Create a new 'player' that will handle our sound. Paused by default.
	player := otoCtx.NewPlayer(decodedMp3)

	// // Play starts playing the sound and returns without waiting for it (Play() is async).
	// player.Play()

	// // We can wait for the sound to finish playing using something like this
	// for player.IsPlaying() {
	// 	time.Sleep(time.Millisecond)
	// }

	// Now that the sound finished playing, we can restart from the beginning (or go to any location in the sound) using seek
	// newPos, err := player.(io.Seeker).Seek(0, io.SeekStart)
	// if err != nil{
	//     panic("player.Seek failed: " + err.Error())
	// }
	// println("Player is now at position:", newPos)
	// player.Play()

	// If you don't want the player/sound anymore simply close
	// err = player.Close()
	// if err != nil {
	// 	panic("player.Close failed: " + err.Error())
	// }

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

		const fragmentShader = `
		#version 330 core
		
		// in vec4  vColor;
		in vec2  vTexCoords;
		// in float vIntensity;
		// in vec4  vClipRect;
		
		out vec4 fragColor;
		
		// uniform vec4 uColorMask;
		uniform vec4 uTexBounds;
		uniform sampler2D uTexture;
		
		void main() {
			fragColor = vec4(1, 0, 0, 0);
			// vec2 t = (vTexCoords - uTexBounds.xy) / uTexBounds.zw;
			// fragColor += vIntensity * vColor * texture(uTexture, t);
			// fragColor *= uColorMask;
		}
		`

		// win.Canvas().SetFragmentShader(fragmentShader)

		imd := imdraw.New(nil)

		shaderImd := imdraw.New(nil)
		shaderCanvas := pixelgl.NewCanvas(win.Bounds())
		shaderCanvas.SetFragmentShader(fragmentShader)
		// shaderCanvas.Draw(win, pixel.IM.Moved(win.Bounds().Center()))

		fps := time.Tick(time.Second / 60)
		start := time.Now()
		nextMeasureDeltaTime := 0

		// Play starts playing the sound and returns without waiting for it (Play() is async).
		// For some reason not running it in goroutine causes it not to play, so we run it in a goroutine
		seekChannel := make(chan int64)
		go func() {
			player.Play()
			// We can wait for the sound to finish playing using something like this
			// for player.IsPlaying() {
			// 	time.Sleep(time.Millisecond)
			// }

			// when told, seek to the new position
			for {
				seekPosition := <-seekChannel
				player.Pause()
				newPos, err := player.Seek(seekPosition, io.SeekStart)
				if err != nil {
					panic("player.Seek failed: " + err.Error())
				}
				logger.Debug("Player is now at position:", newPos)
				player.Play()
			}
		}()

		backgroundColor := colornames.Black
		foregroundColor := colornames.White
		// accentColor := colornames.Black
		for !win.Closed() {
			win.Clear(backgroundColor)
			imd.Clear()
			imd.Reset()
			shaderImd.Clear()
			shaderCanvas.Clear(pixel.RGB(0, 0, 0).Mul(pixel.Alpha(0)))

			if win.JustReleased(pixelgl.KeySpace) {
				start = time.Now()
				nextMeasureDeltaTime = 0
				// TODO: fix the number we send since we now use relative seek
				seekChannel <- 0
			}
			// Gave up on this, can't figure out how to seek in audio properly
			// } else if win.JustReleased(pixelgl.KeyRight) {
			// 	// go one measure forward by moving the start time back by one measure
			// 	timeInMeasure := time.Duration(ppqn) * time.Duration(microSecondsPerQuarterNote) * time.Microsecond
			// 	start = start.Add(timeInMeasure * -1)
			// 	newElapsedTime := time.Since(start)
			// 	// calculate bitrate??
			// 	duration := time.Duration(decodedMp3.Length()) * time.Second / time.Duration(decodedMp3.SampleRate()*2)
			//  // This appears to be incorrect, it printed 10m
			// 	logger.Info("Duration:", duration)

			// 	// calculate bitrate
			// 	fileSizeBytes := decodedMp3.Length()
			// 	fileSizeBits := int64(fileSizeBytes) * 8
			// 	bitrate := fileSizeBits / int64(duration.Seconds())
			// 	byteOffset := (newElapsedTime.Seconds() * float64(bitrate) / 8)

			// 	seekChannel <- int64(byteOffset)
			// }

			// draw some lines for notes
			// for i := 0; i < 128; i++ {
			// 	imd.Color = colornames.Black
			// 	imd.Push(pixel.V(0, float64(i*noteHeight)))
			// 	imd.Push(pixel.V(width, float64(i*noteHeight)))
			// 	imd.Line(1)
			// }

			elapsedSeconds := time.Since(start).Seconds()
			elapsedDeltaTime := secondsToDeltaTime(elapsedSeconds, microSecondsPerQuarterNote, ppqn)

			if elapsedDeltaTime > nextMeasureDeltaTime && oscillateColors {
				nextMeasureDeltaTime += ppqn

				tmp := backgroundColor
				backgroundColor = foregroundColor
				foregroundColor = tmp
			}

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

			for index, t := range tracks {
				colorToUse := trackColors[index%len(trackColors)]

				renderTrack(t, imd, elapsedDeltaTime, noteMin, noteHeight, noteTopBottomPaddingPixels, xScale, xTranslate, colorToUse)

				// renderTrack(t, imd, elapsedDeltaTime, noteMin, noteHeight, noteTopBottomPaddingPixels, xScale/2, xTranslate, colornames.White)
				// renderTrack(t, imd, elapsedDeltaTime, noteMin, noteHeight, noteTopBottomPaddingPixels, xScale, xTranslate, colornames.Red)
				// renderTrack(t, imd, elapsedDeltaTime, noteMin, noteHeight, noteTopBottomPaddingPixels, xScale/2, xTranslate, colornames.Orange)
				// renderTrack(t, imd, elapsedDeltaTime, noteMin, noteHeight, noteTopBottomPaddingPixels, xScale/4, xTranslate, colornames.Yellow)
				// renderTrack(t, imd, elapsedDeltaTime, noteMin, noteHeight, noteTopBottomPaddingPixels, xScale/6, xTranslate, colornames.Green)
				// renderTrack(t, imd, elapsedDeltaTime, noteMin, noteHeight, noteTopBottomPaddingPixels, xScale/8, xTranslate, colornames.Blue)
				// renderTrack(t, imd, elapsedDeltaTime, noteMin, noteHeight, noteTopBottomPaddingPixels, xScale/16, xTranslate, colornames.Indigo)
				// renderTrack(t, imd, elapsedDeltaTime, noteMin, noteHeight, noteTopBottomPaddingPixels, xScale/32, xTranslate, colornames.Violet)
			}

			// vertical line in center
			imd.Color = foregroundColor
			imd.Push(pixel.V(width/2, height), pixel.V(width/2, 0))
			imd.Line(1)

			// measure lines
			for i := 0; i < 200; i++ {
				if i%4 == 0 {
					shaderImd.Color = foregroundColor
					shaderImd.Push(pixel.V(float64(i*ppqn-elapsedDeltaTime)*xScale+xTranslate, height), pixel.V(float64(i*ppqn-elapsedDeltaTime)*xScale+xTranslate, 0))
					shaderImd.Line(1)
				}
			}

			shaderImd.Draw(shaderCanvas)
			shaderCanvas.Draw(win, pixel.IM.Moved(win.Bounds().Center()))
			imd.Draw(win)

			win.Update()
			<-fps
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

		renderAll(tracks, logger)
	} else {
		fmt.Println("Invalid command")
	}
}
