package main

import (
	"master/Driver-go/elevio"
	"master/elevator"
	"master/fsm"
	"master/master"
	"master/network/broadcast"
	"master/network/peers"
	"master/requests"
	"master/types"
	"os"
	"strconv"
	"time"
)

func main() {

	slaveButtonRx := make(chan types.SlaveButtonEventMsg)
	slaveFloorRx := make(chan types.SlaveFloor, 5)
	masterCommandCh := make(chan types.MasterCommand)
	masterSetOrderLightCh := make(chan types.SetOrderLight, 3)
	commandDoorOpenCh := make(chan types.DoorOpen, 3)
	slaveDoorOpenedCh := make(chan types.DoorOpen, 3)
	newEventCh := make(chan types.MasterStruct, 5)
	newActionCh := make(chan types.NewAction, 5)
	peerUpdateCh := make(chan peers.PeerUpdate)
	masterMsgCh := make(chan types.MasterStruct, 3)
	masterInitStructCh := make(chan types.MasterStruct, 2)
	masterMergeSendCh := make(chan types.MasterStruct, 3)
	masterMergeReceiveCh := make(chan types.MasterStruct, 3)
	newMasterIDCh := make(chan types.NewMasterID)
	ableToMoveCh := make(chan types.AbleToMove, 3)

	go broadcast.Receiver(16513, slaveButtonRx)
	go broadcast.Receiver(16514, slaveFloorRx)
	go broadcast.Receiver(16521, slaveDoorOpenedCh)
	go broadcast.Receiver(16527, masterInitStructCh)
	go broadcast.Receiver(16585, masterMergeReceiveCh)
	go broadcast.Receiver(16528, ableToMoveCh)

	go broadcast.Transmitter(16515, masterCommandCh)
	go broadcast.Transmitter(16518, masterSetOrderLightCh)
	go broadcast.Transmitter(16520, commandDoorOpenCh)
	go master.MasterFindNextAction(newEventCh, newActionCh)
	go broadcast.TransmitMasterMsg(16523, masterMsgCh)
	go broadcast.Transmitter(16524, newMasterIDCh)
	go broadcast.Transmitter(16585, masterMergeSendCh)

	go peers.Receiver(16529, peerUpdateCh)

	const interval = 100 * time.Millisecond
	PeriodicNewEventIterator := 0
	var NewPeerList peers.PeerUpdate

	initArg := os.Args[1]
	SlaveID := os.Args[2]
	isolatedArg := os.Args[3]

	var MasterStruct types.MasterStruct
	MasterStruct.ElevStates = map[string]elevator.Elev{}

	if initArg == "init" {
		CurrentFloor := os.Args[4]
		MySlaves := types.MySlaves{Active: []string{SlaveID}}
		MasterStruct = types.MasterStruct{
			CurrentMasterID: SlaveID,
			ProcessID:       os.Getpid(),
			Isolated:        true,
			PeerList:        peers.PeerUpdate{},
			HallRequests:    [][2]bool{{false, false}, {false, false}, {false, false}, {false, false}},
			MySlaves:        MySlaves,
			ElevStates:      map[string]elevator.Elev{},
		}
		MasterStruct.ElevStates[SlaveID] = fsm.UnInitializedElev()
		if entry, ok := MasterStruct.ElevStates[SlaveID]; ok {
			entry.Floor, _ = strconv.Atoi(CurrentFloor)
			MasterStruct.ElevStates[SlaveID] = entry
		}
		for i := 0; i < 5; i++ {
			masterMergeSendCh <- MasterStruct
			time.Sleep(15 * time.Millisecond)
		}
		os.Exit(99)
	}

	for MasterStruct.CurrentMasterID != SlaveID {
		MasterStruct = <-masterInitStructCh
		MasterStruct.ProcessID = os.Getpid()
	}

	IsolatedMasterStruct := MasterStruct
	if isolatedArg == "isolated" {
		go func() {
			for i := 0; i < 5; i++ {
				masterMergeSendCh <- IsolatedMasterStruct
				time.Sleep(300 * time.Millisecond)
			}
		}()
	}

	for {
		select {
		case a := <-ableToMoveCh:
			if a.AbleToMove {
				MasterStruct.MySlaves.Active = master.AppendNoDuplicates(MasterStruct.MySlaves.Active, a.ID)
				MasterStruct.MySlaves.Immobile = master.DeleteElementFromSlice(MasterStruct.MySlaves.Immobile, a.ID)
			} else {
				MasterStruct.MySlaves.Active = master.DeleteElementFromSlice(MasterStruct.MySlaves.Active, a.ID)
				MasterStruct.MySlaves.Immobile = master.AppendNoDuplicates(MasterStruct.MySlaves.Immobile, a.ID)
			}
			newEventCh <- MasterStruct

		case <-time.After(interval):
			masterMsgCh <- MasterStruct
			if PeriodicNewEventIterator == 10 {
				PeriodicNewEventIterator = 0
				for _, slave := range MasterStruct.MySlaves.Active {
					found := false
					for _, peer := range MasterStruct.PeerList.Peers {
						if slave == peer {
							found = true
							break
						}
					}
					if !found {
						MasterStruct.MySlaves.Active = master.DeleteElementFromSlice(MasterStruct.MySlaves.Active, slave)
						MasterStruct.MySlaves.Immobile = master.DeleteElementFromSlice(MasterStruct.MySlaves.Immobile, slave)
					}
				}

				masterMergeSendCh <- MasterStruct
				newEventCh <- MasterStruct
			} else {
				PeriodicNewEventIterator++
			}
		case ReceivedMergeStruct := <-masterMergeReceiveCh:
			if (ReceivedMergeStruct.CurrentMasterID == MasterStruct.CurrentMasterID) && !ReceivedMergeStruct.Isolated {
				if ReceivedMergeStruct.ProcessID != MasterStruct.ProcessID {
					os.Exit(99)
				}
			} else {
				var NextInLine string
				if len(ReceivedMergeStruct.PeerList.Peers) < 2 {
					NextInLine = MasterStruct.CurrentMasterID
				} else {
					NextInLine = ReceivedMergeStruct.PeerList.Peers[0]
				}
				if master.ShouldStayMaster(MasterStruct.CurrentMasterID, NextInLine, MasterStruct.Isolated, ReceivedMergeStruct.Isolated) {
					MasterStruct = master.MergeMasterStructs(MasterStruct, ReceivedMergeStruct)
					HallRequests := MasterStruct.HallRequests
					for k := range MasterStruct.ElevStates {
						CabRequests := MasterStruct.ElevStates[k].CabRequests
						AllRequests := requests.RequestsAppendHallCab(HallRequests, CabRequests)
						SetOrderLight := types.SetOrderLight{MasterID: MasterStruct.CurrentMasterID, ID: k, LightOn: AllRequests}
						masterSetOrderLightCh <- SetOrderLight
					}
				} else {
					for i := 0; i < 5; i++ {
						masterMergeSendCh <- MasterStruct
						time.Sleep(300 * time.Millisecond)
					}
					os.Exit(99)
				}
			}

			newEventCh <- MasterStruct

		case NewPeerList = <-peerUpdateCh:
			MasterStruct.PeerList = NewPeerList
			if len(NewPeerList.Lost) != 0 {
				for k := range NewPeerList.Lost {
					MasterStruct.MySlaves.Active = master.DeleteElementFromSlice(MasterStruct.MySlaves.Active, NewPeerList.Lost[k])
					MasterStruct.MySlaves.Immobile = master.DeleteElementFromSlice(MasterStruct.MySlaves.Immobile, NewPeerList.Lost[k])
				}
				newEventCh <- MasterStruct
			}

		case slaveMsg := <-slaveButtonRx:
			if slaveMsg.Btn_type == 2 {
				if entry, ok := MasterStruct.ElevStates[slaveMsg.ID]; ok {
					entry.CabRequests[slaveMsg.Btn_floor] = true
					MasterStruct.ElevStates[slaveMsg.ID] = entry
				}
			} else {
				MasterStruct.HallRequests[slaveMsg.Btn_floor][slaveMsg.Btn_type] = true
			}
			AllRequests := requests.RequestsAppendHallCab(MasterStruct.HallRequests, MasterStruct.ElevStates[slaveMsg.ID].CabRequests)
			SetOrderLight := types.SetOrderLight{MasterID: MasterStruct.CurrentMasterID, ID: slaveMsg.ID, LightOn: AllRequests}
			newEventCh <- MasterStruct
			masterSetOrderLightCh <- SetOrderLight

		case slaveMsg := <-slaveFloorRx:
			elevatorID := slaveMsg.ID
			elevatorFloor := slaveMsg.NewFloor
			if entry, ok := MasterStruct.ElevStates[elevatorID]; ok {
				entry.Floor = elevatorFloor
				entry.Behaviour = elevator.EB_Idle
				MasterStruct.ElevStates[elevatorID] = entry
			}
			newEventCh <- MasterStruct

		case slaveMsg := <-slaveDoorOpenedCh:
			elevState := MasterStruct.ElevStates[slaveMsg.ID]
			if slaveMsg.SetDoorOpen {
				if entry, ok := MasterStruct.ElevStates[slaveMsg.ID]; ok {
					entry.Behaviour = elevator.EB_DoorOpen
					entry.CabRequests[elevState.Floor] = false
					MasterStruct.ElevStates[slaveMsg.ID] = entry
				}
				elevState = MasterStruct.ElevStates[slaveMsg.ID]
				ClearHallReqs := requests.ShouldClearHallRequest(elevState, MasterStruct.HallRequests)
				MasterStruct.HallRequests[elevState.Floor][elevio.BT_HallUp] = ClearHallReqs[elevio.BT_HallUp]
				MasterStruct.HallRequests[elevState.Floor][elevio.BT_HallDown] = ClearHallReqs[elevio.BT_HallDown]
				AllRequests := requests.RequestsAppendHallCab(MasterStruct.HallRequests, MasterStruct.ElevStates[slaveMsg.ID].CabRequests)
				SetOrderLight := types.SetOrderLight{MasterID: MasterStruct.CurrentMasterID, ID: slaveMsg.ID, LightOn: AllRequests}
				masterSetOrderLightCh <- SetOrderLight
			}
			newEventCh <- MasterStruct

		case a := <-newActionCh:

			newMasterIDCh <- types.NewMasterID{SlaveID: a.ID, NewMasterID: MasterStruct.CurrentMasterID}
			if entry, ok := MasterStruct.ElevStates[a.ID]; ok {
				entry.Behaviour = a.Action.Behaviour
				entry.Dirn = elevio.MotorDirToString(a.Action.Dirn)
				MasterStruct.ElevStates[a.ID] = entry
			}
			if MasterStruct.ElevStates[a.ID].Behaviour == elevator.EB_DoorOpen {
				commandDoorOpenCh <- types.DoorOpen{MasterID: MasterStruct.CurrentMasterID, ID: a.ID, SetDoorOpen: true}
			} else {
				masterCommandCh <- types.MasterCommand{MasterID: MasterStruct.CurrentMasterID, ID: a.ID, Motordir: elevio.MotorDirToString(a.Action.Dirn)}
			}
		}
	}
}
