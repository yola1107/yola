package dfs

type Map struct {
}

//
////可移动路径的全排列: 棋子类型为emType，可移动色子点数为dices的 所有可移动的路径组合
//func (m *Map) Permute(emType int32, dices []int32) [][]int32 {
//	if len(dices) == 0 {
//		return nil
//	}
//	var res [][]int32
//	var cache []int32
//	var visited = make([]bool, len(dices))
//	m.tryFind(&res, cache, emType, dices, visited)
//
//	res1 := [][]int32(nil)
//	res2 := [][]int32(nil)
//
//	for _, v := range res {
//		//一步的路径
//		if len(v) == 2 {
//			if ok, _ := m.canMoveOne(v[0], v[1]); ok {
//				res1 = append(res1, v)
//			}
//		}
//
//		//二步的路径
//		if len(v) == 4 {
//			if ok1, _ := m.canMoveOne(v[0], v[1]); ok1 {
//				m.moveOne(v[0], v[1])
//				if ok2, _ := m.canMoveOne(v[2], v[3]); ok2 {
//					m.moveOne(v[2], v[3])
//					m.backOne()
//					res2 = append(res2, v) // ok && ok
//				}
//				m.backOne()
//			}
//		}
//
//	}
//
//	log.Info("Permute, emType:%+v dices:%+v cnt:%d cnt1:%d cnt2:%d", emType, dices, len(res), len(res1), len(res2))
//
//	if len(res2) == 0 {
//		return res1
//	}
//
//	return res2
//}
//
//func (m *Map) tryFind(res *[][]int32, cache []int32, emType int32, dices []int32, visited []bool) {
//	// 成功找到一组
//	if len(cache) == len(dices) || len(cache)/2 == len(dices) {
//		var c = make([]int32, len(cache))
//		copy(c, cache)
//		*res = append(*res, c)
//		if len(cache)/2 == len(dices) {
//			return
//		}
//	}
//
//	// 回溯
//	for j := 0; j < len(dices); j++ {
//
//		for i := int32(0); i < int32(len(m.stones)); i++ {
//
//			//剪枝
//			//if m.stones[i].emType != emType {
//			//	continue
//			//}
//
//			if !visited[j] {
//				visited[j] = true
//
//				cache = append(cache, i, dices[j])
//				m.tryFind(res, cache, emType, dices, visited)
//				cache = cache[:len(cache)-2]
//
//				visited[j] = false
//			}
//		}
//	}
//}
//
//type TagRow struct {
//	Count  int32
//	EmType int32
//}
//
//func (m *Map) Show() string {
//	cm := m.ToCountMap()
//	b := strings.Builder{}
//
//	for i := int32(0); i <= _maxRow; i++ {
//		if val, ok := cm[i]; ok && val.Count > 0 {
//			str := "W"
//			if cm[i].EmType == EmBLACK {
//				str = "B"
//			}
//			b.WriteString(fmt.Sprintf("|%s%d|", str, cm[i].Count))
//		} else {
//			b.WriteString(fmt.Sprintf("[%d]", i))
//		}
//	}
//	return b.String()
//}
//
//func (m *Map) ToCountMap() map[int32]*TagRow {
//	cm := map[int32]*TagRow{} // key:nRow val:stone
//	for i := int32(0); i <= _maxRow; i++ {
//		cm[i] = &TagRow{
//			Count:  0,
//			EmType: -1,
//		}
//	}
//	for _, v := range m.stones {
//		if c, ok := cm[v.nRow]; ok {
//			cm[v.nRow].Count++
//			cm[v.nRow].EmType = c.EmType
//		}
//	}
//
//	return cm
//}
