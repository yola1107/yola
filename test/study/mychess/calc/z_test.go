//

//            0    1    2    3    4    5    6    7    8
// 	row:0	{'C', 'M', 'X', 'S', 'J', 'S', 'X', 'M', 'C'},
//	row:1	{'0', '0', '0', '0', '0', '0', '0', '0', '0'},
//	row:2	{'0', 'P', '0', '0', '0', '0', '0', 'P', '0'},
//	row:3	{'B', '0', 'B', '0', 'B', '0', 'B', '0', 'B'}, -- red
//	row:4	{'0', '0', '0', '0', '0', '0', '0', '0', '0'}, -- red     river, cross must >= 4
//	row:5	{'0', '0', '0', '0', '0', '0', '0', '0', '0'}, -- black   river, cross must <  4
//	row:6	{'b', '0', 'b', '0', 'b', '0', 'b', '0', 'b'}, -- black
//	row:7	{'0', 'p', '0', '0', '0', '0', '0', 'p', '0'},
//	row:8	{'0', '0', '0', '0', '0', '0', '0', '0', '0'},
//	row:9	{'c', 'm', 'x', 's', 'j', 's', 'x', 'm', 'c'},
//
//

package calc

import (
	"fmt"
	"log"
	"strings"
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	PanelFull := [_maxRow][_maxCol]byte{
		{'C', 'M', 'X', 'S', 'J', 'S', 'X', 'M', 'C'},
		{'0', '0', '0', '0', '0', '0', '0', '0', '0'},
		{'0', 'P', '0', '0', '0', '0', '0', 'P', '0'},
		{'B', '0', 'B', '0', 'B', '0', 'B', '0', 'B'},
		{'0', '0', '0', '0', '0', '0', '0', '0', '0'},
		{'0', '0', '0', '0', '0', '0', '0', '0', '0'},
		{'b', '0', 'b', '0', 'b', '0', 'b', '0', 'b'},
		{'0', 'p', '0', '0', '0', '0', '0', 'p', '0'},
		{'0', '0', '0', '0', '0', '0', '0', '0', '0'},
		{'c', 'm', 'x', 's', 'j', 's', 'x', 'm', 'c'},
	}

	builder := strings.Builder{}
	builder.WriteString("\n")
	for i := 0; i < _maxRow; i++ {
		for j := 0; j < _maxCol; j++ {
			if v := PanelFull[i][j]; v == '0' {
				builder.WriteString(fmt.Sprintf("   "))
			} else {
				builder.WriteString(fmt.Sprintf("%-3c", v))
			}
		}
		builder.WriteString("\n")
	}
	log.Printf("%s", builder.String())
}

func TestNewMap(t *testing.T) {
	m := NewMap()
	for i := int32(0); i < _maxStoneNum; i++ {
		fmt.Printf("%s\n", desc(m.stones[i]))
	}

	show(m)
}

func TestClone(t *testing.T) {
	b := NewMap()

	show(b)

	clone := b.Clone()

	b.killStone(0)
	b.killStone(31)

	show(b)

	b.Reset(clone)
	show(b)

	clone.killStone(2)
	show(clone)
	show(b)

}

func TestMustKill(t *testing.T) {

	const (
		testCnt = 1
	)

	var (
		okCnt  = 0
		errCnt = 0
		start  = time.Now()
	)

	for i := 0; i < testCnt; i++ {

		m := NewMap()

		show(m)

		dead := m.mustKill(CampBLACK)

		if !dead {
			okCnt++
		} else {
			errCnt++
		}

		show(m)
	}

	fmt.Printf("\nerrCnt:%d testCnt:%d useTime:%v\n\n\n", errCnt, testCnt, time.Since(start))

}

func TestAiMove(t *testing.T) {

	var cnt int

	var over bool

	var step *Step

	var m = NewMap()
	var a1 = NewAiTest(CampRED, m)
	var a2 = NewAiTest(CampBLACK, m)

	show(m)

	for !over {
		cnt++

		if m.beRedTurn {
			log.Printf("     第[%d]步 -- 红方下棋         \n", cnt)
			step = a1.Move()

		} else {
			log.Printf("     第[%d]步 -- 黑方下棋         \n", cnt)
			step = a2.Move()
		}

		if step != nil {
			if step.killId == _noExistId {
				log.Printf("     %s%s(%d,%d)-->(%d,%d) ", descCamp[step.moveId/16], m.stones[step.moveId].sDesc,
					step.fromRow, step.fromCol, step.toRow, step.toCol)

			} else {
				log.Printf("     %s%s(%d,%d)-->%s%s(%d,%d) ", descCamp[step.moveId/16], m.stones[step.moveId].sDesc,
					step.fromRow, step.fromCol, descCamp[step.killId/16], m.stones[int(step.killId)].sDesc, step.toRow, step.toCol)
			}

		} else {
			over = true

			log.Printf("ERRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRRR\n")
		}

		show2(m)

		time.Sleep(5 * time.Second)

	}

}

func show(m *Map) {
	builder := strings.Builder{}
	builder.WriteString("\n")
	for i := int32(0); i < _maxRow; i++ {
		for j := int32(0); j < _maxCol; j++ {
			if id := m.GetStoneId(i, j); id == _noExistId {
				builder.WriteString(fmt.Sprintf("        "))
			} else {
				builder.WriteString(fmt.Sprintf("%-8s", desc(m.stones[id])))
			}
		}
		builder.WriteString("\n")
	}
	log.Printf("%s", builder.String())
	fmt.Printf("\n-------------------------------------------------------------\n")
}

func desc(s Stone) string {
	str := fmt.Sprintf("(%d%d%s%d%d)", s.nRow, s.nCol, s.sDesc, s.nId, s.nCamp)
	return str
}

func show2(m *Map) {

	var _descStone = map[int32]string{
		CHE:   "c",
		MA:    "m",
		PAO:   "@",
		BING:  "b",
		JIANG: "j",
		SHI:   "s",
		XIANG: "x",

		CHE + 10:   "C",
		MA + 10:    "M",
		PAO + 10:   "P",
		BING + 10:  "B",
		JIANG + 10: "J",
		SHI + 10:   "S",
		XIANG + 10: "X",
	}

	builder := strings.Builder{}
	builder.WriteString("\n")
	for i := int32(0); i < _maxRow; i++ {
		for j := int32(0); j < _maxCol; j++ {
			if id := m.GetStoneId(i, j); id == _noExistId {
				builder.WriteString(fmt.Sprintf("     "))
			} else {
				s := m.stones[id]
				emt := s.emType + s.nCamp*10
				builder.WriteString(fmt.Sprintf("%5s", _descStone[emt]))
			}
		}
		builder.WriteString("\n")
	}
	log.Printf("%s", builder.String())
	fmt.Printf("\n-------------------------------------------------------------\n")
}

//func (m *Map) Show() string {
//	var _descStone = map[int32]string{
//		CHE:   "c",
//		MA:    "m",
//		PAO:   "p`",
//		BING:  "b",
//		JIANG: "j",
//		SHI:   "s",
//		XIANG: "x",
//
//		CHE + 10:   "C",
//		MA + 10:    "M",
//		PAO + 10:   "P",
//		BING + 10:  "B",
//		JIANG + 10: "J",
//		SHI + 10:   "S",
//		XIANG + 10: "X",
//	}
//
//	b := strings.Builder{}
//	b.WriteString("-------------------------------------------------------------\n")
//	for i := int32(0); i < _maxRow; i++ {
//		for j := int32(0); j < _maxCol; j++ {
//			if id := m.GetStoneId(i, j); id == _noExistId {
//				b.WriteString(fmt.Sprintf("     "))
//			} else {
//				s := m.stones[id]
//				emt := s.emType + s.nCamp*10
//				b.WriteString(fmt.Sprintf("%5s", _descStone[emt]))
//			}
//		}
//		b.WriteString("\n")
//	}
//	b.WriteString("-------------------------------------------------------------\n")
//	//log.Printf("%s", builder.String())
//	return b.String()
//}
