package main

import (
	"encoding/binary"
	"fmt"
	"os"

	"gitlab.com/gomidi/midi/v2/smf"
)

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

func readVariableLengthValue(d []byte, start int) (result int, offset int) {
	result = 0
	i := start
	for i < len(d) {
		// add the lower 7 bits of the current byte to the result
		result = (result << 7) | int(d[i]&0x7F)

		// if bit 7 is not set, this is the last byte of the value
		if d[i]&0x80 == 0 {
			break
		}

		i++
	}

	// return result, i + 1 - start
	return result, i + 1
}

func diy() {
	// Reference: https://midimusic.github.io/tech/midispec.html
	dat, err := os.ReadFile("test1.mid")
	check(err)
	fmt.Println(string(dat))

	// first 4 bytes (32 bits) are the header type in ascii
	headerType := dat[0:4]
	fmt.Println("Header Type:", string(headerType))

	// length is the next 4 bytes (32 bits) in big endian
	length := dat[4:8]
	lengthInt := binary.BigEndian.Uint32(length)
	fmt.Println("Length:", lengthInt)

	// -- Data Section --
	// format is the next 2 bytes (16 bits) in big endian
	format := dat[8:10]
	formatInt := binary.BigEndian.Uint16(format)
	fmt.Println("Format:", formatInt)
	if formatInt != 0 {
		panic("Format not supported")
	}

	// ntracks is the next 2 bytes (16 bits) in big endian
	ntracks := dat[10:12]
	ntracksInt := binary.BigEndian.Uint16(ntracks)
	fmt.Println("NTracks:", ntracksInt)

	// division is the next 2 bytes (16 bits) in big endian
	// if the first bit is 0, the remaining 15 bits represent the number of ticks quarter note
	//   For instance, if division is 96, then a time interval of an eighth-note between two events in the file would be 48
	// if the first bit is 1, the remaining 15 bits represent the number of ticks per frame
	divisionType := dat[12]
	fmt.Println("Division Type:", divisionType)

	if divisionType&0x80 == 0 {
		division := binary.BigEndian.Uint16(dat[12:14])
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
	trackHeader := dat[14:18]
	fmt.Println("Track Header:", string(trackHeader))

	// track length is the next 4 bytes (32 bits) in big endian
	trackLength := dat[18:22]
	trackLengthInt := binary.BigEndian.Uint32(trackLength)
	fmt.Println("Track Length:", trackLengthInt)

	// <MTrk event> = <delta-time><event>
	// <delta-time> is stored as a variable-length quantity.
	// It represents the amount of time before the following event.
	// 	If the first event in a track occurs at the very beginning of a track, or if two events occur simultaneously, a delta-time of zero is used. Delta-times are always present.
	// (Not storing delta-times of 0 requires at least two bytes for any other value, and most delta-times aren't zero.)
	// Delta-time is in some fraction of a beat (or a second, for recording a track with SMPTE times), as specified in the header chunk.
	// <event> = <MIDI event> | <sysex event> | <meta-event>
	// Print only note on and note offf midi events and their data as well as delta time events
	i := 22
	eventsToRead := 3
	for i < len(dat) && eventsToRead > 0 {
		eventsToRead--
		fmt.Println("------- EVENT -------")

		deltaTime, offset := readVariableLengthValue(dat, i)
		fmt.Println("Delta Time:", deltaTime)
		fmt.Println("Offset:", offset)
		// i += offset
		i = offset
		if i >= len(dat) {
			break
		}

		fmt.Printf("Event first byte: %x\n", dat[i])

		if dat[i] == 0xFF {
			// <meta-event> = 0xFF<type><length><data>
			metaEventType := dat[i+1]
			metaEventLength, offset := readVariableLengthValue(dat, i+2)
			fmt.Printf("Meta Event Type: %x\n", metaEventType)
			fmt.Println("Meta Event Length:", metaEventLength)
			// i += offset + 2
			i = offset
		} else if dat[i] == 0xF0 || dat[i] == 0xF7 {
			// <sysex event> = 0xF0<length><data> or 0xF7<length><data>
			sysexEventLength, offset := readVariableLengthValue(dat, i+1)
			fmt.Println("Sysex Event Length:", sysexEventLength)
			// i += offset + 1
			i = offset
		} else {
			// <MIDI event> = <MIDI event type><channel><data>
			// <MIDI event type> = <MIDI event type (4 bits)><MIDI channel (4 bits)>
			// <MIDI event type> = 0x8 for note off, 0x9 for note on
			midiEventType := dat[i]
			fmt.Printf("RAW MIDI Event Type: %x\n", midiEventType)
			i += 1
			// midiChannel := midiEventType & 0x0F
			midiEventType = midiEventType >> 4

			switch midiEventType {
			case 0x8:
				{
					fmt.Println("MIDI Event Type: Note Off")
					i++
					// <data> = <note><velocity>
					note := dat[i]
					velocity := dat[i+1]
					fmt.Println("  Note:", note, noteNumberToString(note))
					fmt.Println("  Velocity:", velocity)
					i += 2
					break
				}
			case 0x9:
				{
					fmt.Println("MIDI Event Type: Note On")
					i++
					// <data> = <note><velocity>
					note := dat[i]
					velocity := dat[i+1]
					fmt.Println("  Note:", note, noteNumberToString(note))
					fmt.Println("  Velocity:", velocity)
					i += 2
					break
				}
			default:
				fmt.Printf("Unhandled MIDI Event Type: %x\n", midiEventType)
				// panic("Unhandled MIDI Event Type")
			}
		}
	}
}

func pkg() {
	reader, err := smf.ReadFile("test1.mid")
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
		diy()
	} else {
		fmt.Println("Invalid command")
	}
}
