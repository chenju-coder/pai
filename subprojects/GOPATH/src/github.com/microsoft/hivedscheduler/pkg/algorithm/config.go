// MIT License
//
// Copyright (c) Microsoft Corporation. All rights reserved.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE

package algorithm

import (
	"fmt"
	"github.com/microsoft/hivedscheduler/pkg/api"
	"github.com/microsoft/hivedscheduler/pkg/common"
	"strings"
)

// internal wrapper for spec cellTypes
type cellChainElement struct {
	cellType      api.CellType // current cell type
	level         CellLevel    // current cell level, leaf cell is 1
	childCellType api.CellType // child cell type
	childNumber   int32        // child number
	hasNode       bool         // current cell type is a node or above cell
	isMultiNodes  bool         // current cell type is a multiple node cell
	gpuType       string       // current cell gpu type
	gpuNumber     int32        // how many gpu in current cell
}

type cellTypeConstructor struct {
	// input: raw spec from config
	cellTypeSpecs map[api.CellType]api.CellTypeSpec
	// output: converted wrapper
	cellChainElements map[api.CellType]*cellChainElement
}

func newCellTypeConstructor(cellTypes map[api.CellType]api.CellTypeSpec) *cellTypeConstructor {
	return &cellTypeConstructor{
		cellTypeSpecs:     cellTypes,
		cellChainElements: map[api.CellType]*cellChainElement{},
	}
}

func (c *cellTypeConstructor) addCellChain(ct api.CellType) {
	_, ok := c.cellChainElements[ct]
	if ok {
		// already added
		return
	}

	ctSpec, ok := c.cellTypeSpecs[ct]
	if !ok {
		// not found in raw spec, it's leaf cell
		c.cellChainElements[ct] = &cellChainElement{
			cellType:      ct,
			level:         lowestLevel,
			childCellType: "",
			childNumber:   0,
			hasNode:       false,
			isMultiNodes:  false,
			gpuType:       string(ct),
			gpuNumber:     1,
		}
		return
	}

	// recursively add children
	child := ctSpec.ChildCellType
	if _, ok := c.cellChainElements[child]; !ok {
		c.addCellChain(child)
	}

	// child cell type has been added, added current element,
	cct := c.cellChainElements[child]
	c.cellChainElements[ct] = &cellChainElement{
		cellType:      ct,
		level:         cct.level + 1,
		childCellType: cct.cellType,
		childNumber:   ctSpec.ChildCellNumber,
		hasNode:       cct.hasNode || ctSpec.IsNodeLevel,
		isMultiNodes:  cct.hasNode,
		gpuType:       cct.gpuType,
		gpuNumber:     cct.gpuNumber * ctSpec.ChildCellNumber,
	}
	return
}

func (c *cellTypeConstructor) buildCellChains() map[api.CellType]*cellChainElement {
	for p := range c.cellTypeSpecs {
		c.addCellChain(p)
	}
	return c.cellChainElements
}

type physicalCellConstructor struct {
	// input
	cellChainElements map[api.CellType]*cellChainElement
	cellChainSpecs    []api.PhysicalCellSpec
	// output
	physicalCellList map[CellChain]ChainCellList
	reservedCells    map[api.ReservationId]*PhysicalCell
	// internal status
	buildingChain CellChain            // current build chain, it the top cell type in physicalCells
	buildingSpec  api.PhysicalCellSpec // current building spec instance
}

func newPhysicalCellConstructor(
	cellChainElements map[api.CellType]*cellChainElement,
	cellChainSpecs []api.PhysicalCellSpec) *physicalCellConstructor {

	return &physicalCellConstructor{
		cellChainElements: cellChainElements,
		cellChainSpecs:    cellChainSpecs,
		physicalCellList:  map[CellChain]ChainCellList{},
		reservedCells:     map[api.ReservationId]*PhysicalCell{},
	}
}

func (c *physicalCellConstructor) updateInternalStatus(buildingChain CellChain, buildingSpec api.PhysicalCellSpec) {
	c.buildingChain = buildingChain
	c.buildingSpec = buildingSpec
}

func (c *physicalCellConstructor) buildChildCell(
	spec api.PhysicalCellSpec,
	ct api.CellType,
	currentNode api.CellAddress,
	addressPrefix string) *PhysicalCell {

	ce := c.cellChainElements[ct]
	var address string
	if ce.hasNode && !ce.isMultiNodes {
		// node-level cell pass address to children as node
		currentNode = spec.CellAddress
		address = string(spec.CellAddress)
	}
	if !ce.hasNode {
		address = addressPrefix + "/" + string(spec.CellAddress)
	}
	cellInstance := c.addCell(c.buildingChain, ce, spec.ReservationId, address)
	if ce.level == 1 {
		cellInstance.SetPhysicalResources(
			[]string{string(currentNode)}, []int32{common.StringToInt32(string(spec.CellAddress))})
		return cellInstance
	}
	var currentCellNodes []string
	var currentCellGpuIndices []int32
	var currentCellChildren CellList
	for _, childSpec := range spec.CellChildren {
		childCellInstance := c.buildChildCell(childSpec, ce.childCellType, currentNode, address)
		childCellInstance.SetParent(cellInstance)
		currentCellChildren = append(currentCellChildren, childCellInstance)
		if ce.isMultiNodes {
			// super-node cell merge child nodes
			currentCellNodes = append(currentCellNodes, childCellInstance.nodes...)
		} else {
			// sub-node cell merge child node gpu indices
			currentCellGpuIndices = append(currentCellGpuIndices, childCellInstance.gpuIndices...)
		}
	}
	// update current cell children and resource
	cellInstance.SetChildren(currentCellChildren)
	if ce.isMultiNodes {
		currentCellGpuIndices = []int32{-1}
		cellInstance.SetAddress(strings.Join(currentCellNodes, ":"))
	} else {
		currentCellNodes = []string{string(currentNode)}
	}
	cellInstance.SetPhysicalResources(currentCellNodes, currentCellGpuIndices)

	return cellInstance
}

func (c *physicalCellConstructor) addCell(
	chain CellChain,
	ce *cellChainElement,
	reservationId api.ReservationId,
	address string) *PhysicalCell {

	cellInstance := NewPhysicalCell(c.buildingChain, ce.level, ce.hasNode, ce.gpuNumber, string(ce.cellType), address)
	if _, ok := c.physicalCellList[chain]; !ok {
		c.physicalCellList[chain] = ChainCellList{}
	}
	c.physicalCellList[chain][ce.level] = append(c.physicalCellList[chain][ce.level], cellInstance)
	// record and mark reserved cell
	if reservationId != "" {
		c.reservedCells[reservationId] = cellInstance
		cellInstance.SetReserved(true)
	}
	return cellInstance
}

func (c *physicalCellConstructor) buildFullTree() {
	cc := c.buildingChain
	ce, ok := c.cellChainElements[api.CellType(cc)]
	if !ok {
		panic(fmt.Sprintf("cellType %v in PhysicalCells is not found in cell types definition", cc))
	}
	if !ce.hasNode {
		panic(fmt.Sprintf("top cell must be node-level or above: %v", cc))
	}
	cellInstance := c.buildChildCell(c.buildingSpec, api.CellType(cc), c.buildingSpec.CellAddress, "")
	// set GPU type only for top-level cells (as a chain shares the same GPU type)
	cellInstance.GetStatus().GpuType = ce.gpuType
}

func (c *physicalCellConstructor) build() (map[CellChain]ChainCellList, map[api.ReservationId]*PhysicalCell) {
	for _, spec := range c.cellChainSpecs {
		c.updateInternalStatus(CellChain(spec.CellType), spec)
		c.buildFullTree()
	}
	return c.physicalCellList, c.reservedCells
}

type virtualCellConstructor struct {
	// input
	cellChainElements        map[api.CellType]*cellChainElement
	specs                    map[api.VirtualClusterName]api.VirtualClusterSpec
	rawReservedPhysicalCells map[api.ReservationId]*PhysicalCell // rId:physicalCell
	// output
	virtualNonReservedCellList map[api.VirtualClusterName]map[CellChain]ChainCellList         // vc:cellChain:cellLevel:virtualCells
	virtualReservedCellList    map[api.VirtualClusterName]map[api.ReservationId]ChainCellList // vc:rId:cellLevel:virtualCells
	reservedPhysicalCells      map[api.VirtualClusterName]map[api.ReservationId]*PhysicalCell // vc:rId:physicalCell
	// internal status
	buildingVc          api.VirtualClusterName // current building vc
	buildingChain       CellChain              // current building chain, it's a in a.b.c
	buildingChild       api.CellType           // current building child, it's c in a.b.c
	buildingRoot        *VirtualCell           // current building root cell, it's instance of c in a.b.c
	buildingReservation api.ReservationId      // current building is a reservation
}

func newVirtualCellConstructor(
	cellChains map[api.CellType]*cellChainElement,
	specs map[api.VirtualClusterName]api.VirtualClusterSpec,
	reservedCells map[api.ReservationId]*PhysicalCell) *virtualCellConstructor {

	return &virtualCellConstructor{
		cellChainElements:          cellChains,
		specs:                      specs,
		rawReservedPhysicalCells:   reservedCells,
		virtualNonReservedCellList: map[api.VirtualClusterName]map[CellChain]ChainCellList{},
		virtualReservedCellList:    map[api.VirtualClusterName]map[api.ReservationId]ChainCellList{},
		reservedPhysicalCells:      map[api.VirtualClusterName]map[api.ReservationId]*PhysicalCell{},
	}
}

func (c *virtualCellConstructor) updateInternalStatus(buildingVc api.VirtualClusterName, buildingChain CellChain,
	buildingChild api.CellType, buildingRoot *VirtualCell, buildingReservation api.ReservationId) {
	c.buildingVc = buildingVc
	c.buildingChain = buildingChain
	c.buildingChild = buildingChild
	c.buildingRoot = buildingRoot
	c.buildingReservation = buildingReservation
}

func (c *virtualCellConstructor) addCell(
	chain CellChain,
	vc api.VirtualClusterName,
	ce *cellChainElement,
	address string) *VirtualCell {

	cellInstance := NewVirtualCell(vc, c.buildingChain, ce.level, ce.hasNode, ce.gpuNumber, nil, string(ce.cellType), address)
	if c.buildingReservation == "" {
		if _, ok := c.virtualNonReservedCellList[vc]; !ok {
			c.virtualNonReservedCellList[vc] = map[CellChain]ChainCellList{}
		}
		if _, ok := c.virtualNonReservedCellList[vc][chain]; !ok {
			c.virtualNonReservedCellList[vc][chain] = ChainCellList{}
		}
		c.virtualNonReservedCellList[vc][chain][ce.level] = append(c.virtualNonReservedCellList[vc][chain][ce.level], cellInstance)
	} else {
		rId := c.buildingReservation
		if _, ok := c.virtualReservedCellList[vc]; !ok {
			c.virtualReservedCellList[vc] = map[api.ReservationId]ChainCellList{}
		}
		if _, ok := c.virtualReservedCellList[vc][rId]; !ok {
			c.virtualReservedCellList[vc][rId] = ChainCellList{}
		}
		c.virtualReservedCellList[vc][rId][ce.level] = append(c.virtualReservedCellList[vc][rId][ce.level], cellInstance)
	}
	if c.buildingRoot == nil {
		c.buildingRoot = cellInstance
	}
	cellInstance.SetPreAssignedCell(c.buildingRoot)
	return cellInstance
}

func (c *virtualCellConstructor) buildChildCell(ct api.CellType, address string) *VirtualCell {
	ce := c.cellChainElements[ct]
	cellInstance := c.addCell(c.buildingChain, c.buildingVc, ce, address)
	if ce.level == 1 {
		return cellInstance
	}
	var currentCellChildren CellList
	splitAddress := strings.Split(address, "/")
	offset := common.StringToInt32(splitAddress[len(splitAddress)-1]) * ce.childNumber
	for i := int32(0); i < ce.childNumber; i++ {
		childCellInstance := c.buildChildCell(ce.childCellType, fmt.Sprintf("%v/%v", address, offset+i))
		childCellInstance.SetParent(cellInstance)
		currentCellChildren = append(currentCellChildren, childCellInstance)
	}
	cellInstance.SetChildren(currentCellChildren)
	return cellInstance
}

func (c *virtualCellConstructor) buildFullTree(address string) {
	ce, ok := c.cellChainElements[c.buildingChild]
	if !ok {
		panic(fmt.Sprintf("cellType %v in VirtualCells is not found in cell types definition", c.buildingChild))
	}
	cellInstance := c.buildChildCell(c.buildingChild, address)
	// set GPU type only for top-level cells (as a chain shares the same GPU type)
	cellInstance.GetStatus().GpuType = ce.gpuType
}

func (c *virtualCellConstructor) build() (
	map[api.VirtualClusterName]map[CellChain]ChainCellList,
	map[api.VirtualClusterName]map[api.ReservationId]ChainCellList,
	map[api.VirtualClusterName]map[api.ReservationId]*PhysicalCell) {

	for vc, spec := range c.specs {
		numCells := int32(0)
		c.virtualNonReservedCellList[vc] = map[CellChain]ChainCellList{}
		c.virtualReservedCellList[vc] = map[api.ReservationId]ChainCellList{}
		c.reservedPhysicalCells[vc] = map[api.ReservationId]*PhysicalCell{}

		for _, virtualCell := range spec.VirtualCells {
			sl := strings.Split(string(virtualCell.CellType), ".")
			for i := int32(0); i < virtualCell.CellNumber; i++ {
				c.updateInternalStatus(vc, CellChain(sl[0]), api.CellType(sl[len(sl)-1]), nil, "")
				c.buildFullTree(fmt.Sprintf("%v/%v", vc, numCells))
				numCells++
			}
		}

		for _, reservedCell := range spec.ReservedCells {
			rid := reservedCell.ReservationId
			pc, ok := c.rawReservedPhysicalCells[rid]
			if !ok {
				panic(fmt.Sprintf("reservationId not found in physicalCells: VC: %v, ID: %v", vc, rid))
			}
			c.reservedPhysicalCells[vc][rid] = pc
			// get cellType by reservationId
			buildingChild := api.CellType(pc.chain)
			for c.cellChainElements[buildingChild].level > pc.level {
				buildingChild = c.cellChainElements[buildingChild].childCellType
			}

			c.updateInternalStatus(vc, pc.chain, buildingChild, nil, rid)
			c.buildFullTree(fmt.Sprintf("%v/%v", vc, numCells))
			numCells++
		}
	}
	return c.virtualNonReservedCellList, c.virtualReservedCellList, c.reservedPhysicalCells
}

func parseCellChainInfo(
	cellChainElements map[api.CellType]*cellChainElement,
	chains []CellChain) (
	map[CellChain]map[CellLevel]int32,
	map[CellChain]map[CellLevel]api.CellType,
	map[string][]CellChain) {

	cellLevelToGpuNum := map[CellChain]map[CellLevel]int32{}
	cellLevelToType := map[CellChain]map[CellLevel]api.CellType{}
	gpuTypeToChain := map[string][]CellChain{}
	for _, chain := range chains {
		ce := cellChainElements[api.CellType(chain)]
		gpuTypeToChain[ce.gpuType] = append(gpuTypeToChain[ce.gpuType], chain)

		cellLevelToGpuNum[chain] = map[CellLevel]int32{}
		cellLevelToType[chain] = map[CellLevel]api.CellType{}
		ce, ok := cellChainElements[api.CellType(chain)]
		for ok {
			cellLevelToGpuNum[chain][ce.level] = ce.gpuNumber
			cellLevelToType[chain][ce.level] = ce.cellType
			ce, ok = cellChainElements[ce.childCellType]
		}
	}
	return cellLevelToGpuNum, cellLevelToType, gpuTypeToChain

}

func ParseConfig(sConfig *api.Config) (
	map[CellChain]ChainCellList, // chain:level:[]physicalCell
	map[CellChain]map[CellLevel]int32, // chain:level:gpuNumber
	map[string][]CellChain, // gpuType:[]chain
	map[CellChain]map[CellLevel]api.CellType, // chain:level:cellType
	map[api.VirtualClusterName]map[CellChain]ChainCellList, // non reserved virtual cells, vc:chain:level:[]virtualCell
	map[api.VirtualClusterName]map[api.ReservationId]ChainCellList, // reserved virtual cells, vc:reservationId:level:[]virtualCell
	map[api.VirtualClusterName]map[api.ReservationId]*PhysicalCell, // vc:reservationId:PhysicalCell
) {
	cellTypes := sConfig.PhysicalCluster.CellTypes
	cellChainElements := newCellTypeConstructor(cellTypes).buildCellChains()

	physicalSpecs := sConfig.PhysicalCluster.PhysicalCells
	physicalCells, rawReservedPhysicalCells := newPhysicalCellConstructor(cellChainElements, physicalSpecs).build()

	cellChains := make([]CellChain, 0, len(physicalCells))
	for k := range physicalCells {
		cellChains = append(cellChains, k)
	}
	cellLevelToGpuNum, cellLevelToType, gpuTypeToChain := parseCellChainInfo(cellChainElements, cellChains)

	virtualSpecs := sConfig.VirtualClusters
	virtualNonReservedCellList, virtualReservedCellList, reservedPhysicalCells := newVirtualCellConstructor(
		cellChainElements, *virtualSpecs, rawReservedPhysicalCells).build()

	return physicalCells, cellLevelToGpuNum, gpuTypeToChain, cellLevelToType, virtualNonReservedCellList, virtualReservedCellList, reservedPhysicalCells
}
