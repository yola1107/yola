package calc

import (
	"fmt"
)

const (
	CHE   = 1
	MA    = 2
	PAO   = 3
	BING  = 4
	JIANG = 5
	SHI   = 6
	XIANG = 7
)

const (
	CampRED = iota
	CampBLACK
)

var descStone = map[int32]string{
	CHE:   "车",
	MA:    "马",
	PAO:   "炮",
	BING:  "兵",
	JIANG: "将",
	SHI:   "士",
	XIANG: "相",
}

var descCamp = map[int32]string{
	CampRED:   "红",
	CampBLACK: "黑",
}

var tPos = [16]stPos{
	{0, 0, CHE},
	{0, 1, MA},
	{0, 2, XIANG},
	{0, 3, SHI},
	{0, 4, JIANG},
	{0, 5, SHI},
	{0, 6, XIANG},
	{0, 7, MA},
	{0, 8, CHE},
	{2, 1, PAO},
	{2, 7, PAO},
	{3, 0, BING},
	{3, 2, BING},
	{3, 4, BING},
	{3, 6, BING},
	{3, 8, BING},
}

type stPos struct {
	nRow   int32
	nCol   int32
	emType int32
}

type Stone struct {
	nId    int32
	nRow   int32
	nCol   int32
	nCamp  int32
	emType int32
	bDead  bool
	sDesc  string
}

func (s *Stone) init(id int32) {
	if id < 16 {
		s.nRow = tPos[id].nRow
		s.nCol = tPos[id].nCol
		s.emType = tPos[id].emType
	} else {
		s.nRow = 9 - tPos[id-16].nRow
		s.nCol = 8 - tPos[id-16].nCol
		s.emType = tPos[id-16].emType
	}

	s.nId = int32(id)
	s.nCamp = id / 16
	s.sDesc = descStone[s.emType]

}
func (s *Stone) rotate() {
	s.nRow = 9 - s.nRow
	s.nCol = 8 - s.nCol
}
func (s *Stone) GetId() int32 {
	return s.nId
}
func (s *Stone) GetRow() int32 {
	return s.nRow
}
func (s *Stone) GetCol() int32 {
	return s.nCol
}
func (s *Stone) GetEmType() int32 {
	return s.emType
}
func (s *Stone) IsDead() bool {
	return s.bDead
}
func (s *Stone) GetCamp() int32 {
	return s.nCamp
}
func (s *Stone) GetDesc() string {
	return s.sDesc
}
func (s *Stone) GetFulDesc() string {
	if s == nil {
		return ""
	}
	return fmt.Sprintf("`%d`%s%s", s.nId, descCamp[s.nCamp], descStone[s.emType], )
}
