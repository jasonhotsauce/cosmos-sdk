package keeper

import (
	"fmt"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/stake/types"
	tmtypes "github.com/tendermint/tendermint/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetValidator(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 10)
	pool := keeper.GetPool(ctx)

	// test how the validator is set from a purely unbonbed pool
	validator := types.NewValidator(addrVals[0], PKs[0], types.Description{})
	validator, pool, _ = validator.AddTokensFromDel(pool, sdk.NewInt(10))
	require.Equal(t, sdk.Unbonded, validator.Status)
	assert.True(sdk.DecEq(t, sdk.NewDec(10), validator.Tokens))
	assert.True(sdk.DecEq(t, sdk.NewDec(10), validator.DelegatorShares))
	keeper.SetPool(ctx, pool)
	keeper.UpdateValidator(ctx, validator)

	// after the save the validator should be bonded
	validator, found := keeper.GetValidator(ctx, addrVals[0])
	require.True(t, found)
	require.Equal(t, sdk.Bonded, validator.Status)
	assert.True(sdk.DecEq(t, sdk.NewDec(10), validator.Tokens))
	assert.True(sdk.DecEq(t, sdk.NewDec(10), validator.DelegatorShares))

	// Check each store for being saved
	resVal, found := keeper.GetValidator(ctx, addrVals[0])
	assert.True(ValEq(t, validator, resVal))
	require.True(t, found)

	resVals := keeper.GetValidatorsBonded(ctx)
	require.Equal(t, 1, len(resVals))
	assert.True(ValEq(t, validator, resVals[0]))

	resVals = keeper.GetValidatorsByPower(ctx)
	require.Equal(t, 1, len(resVals))
	assert.True(ValEq(t, validator, resVals[0]))

	updates := keeper.GetTendermintUpdates(ctx)
	require.Equal(t, 1, len(updates))
	require.Equal(t, validator.ABCIValidator(), updates[0])
}

func TestUpdateValidatorByPowerIndex(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 0)
	pool := keeper.GetPool(ctx)

	// create a random pool
	pool.LooseTokens = sdk.NewDec(10000)
	pool.BondedTokens = sdk.NewDec(1234)
	keeper.SetPool(ctx, pool)

	// add a validator
	validator := types.NewValidator(addrVals[0], PKs[0], types.Description{})
	validator, pool, delSharesCreated := validator.AddTokensFromDel(pool, sdk.NewInt(100))
	require.Equal(t, sdk.Unbonded, validator.Status)
	require.Equal(t, int64(100), validator.Tokens.RoundInt64())
	keeper.SetPool(ctx, pool)
	keeper.UpdateValidator(ctx, validator)
	validator, found := keeper.GetValidator(ctx, addrVals[0])
	require.True(t, found)
	require.Equal(t, int64(100), validator.Tokens.RoundInt64(), "\nvalidator %v\npool %v", validator, pool)

	pool = keeper.GetPool(ctx)
	power := GetValidatorsByPowerIndexKey(validator, pool)
	require.True(t, keeper.validatorByPowerIndexExists(ctx, power))

	// burn half the delegator shares
	validator, pool, burned := validator.RemoveDelShares(pool, delSharesCreated.Quo(sdk.NewDec(2)))
	require.Equal(t, int64(50), burned.RoundInt64())
	keeper.SetPool(ctx, pool)              // update the pool
	keeper.UpdateValidator(ctx, validator) // update the validator, possibly kicking it out
	require.False(t, keeper.validatorByPowerIndexExists(ctx, power))

	pool = keeper.GetPool(ctx)
	validator, found = keeper.GetValidator(ctx, addrVals[0])
	require.True(t, found)
	power = GetValidatorsByPowerIndexKey(validator, pool)
	require.True(t, keeper.validatorByPowerIndexExists(ctx, power))
}

func TestUpdateBondedValidatorsDecreaseCliff(t *testing.T) {
	numVals := 10
	maxVals := 5

	// create context, keeper, and pool for tests
	ctx, _, keeper := CreateTestInput(t, false, 0)
	pool := keeper.GetPool(ctx)

	// create keeper parameters
	params := keeper.GetParams(ctx)
	params.MaxValidators = uint16(maxVals)
	keeper.SetParams(ctx, params)

	// create a random pool
	pool.LooseTokens = sdk.NewDec(10000)
	pool.BondedTokens = sdk.NewDec(1234)
	keeper.SetPool(ctx, pool)

	validators := make([]types.Validator, numVals)
	for i := 0; i < len(validators); i++ {
		moniker := fmt.Sprintf("val#%d", int64(i))
		val := types.NewValidator(Addrs[i], PKs[i], types.Description{Moniker: moniker})
		val.BondHeight = int64(i)
		val.BondIntraTxCounter = int16(i)
		val, pool, _ = val.AddTokensFromDel(pool, sdk.NewInt(int64((i+1)*10)))

		keeper.SetPool(ctx, pool)
		val = keeper.UpdateValidator(ctx, val)
		validators[i] = val
	}

	nextCliffVal := validators[numVals-maxVals+1]

	// remove enough tokens to kick out the validator below the current cliff
	// validator and next in line cliff validator
	nextCliffVal, pool, _ = nextCliffVal.RemoveDelShares(pool, sdk.NewDec(21))
	keeper.SetPool(ctx, pool)
	nextCliffVal = keeper.UpdateValidator(ctx, nextCliffVal)

	// require the cliff validator has changed
	cliffVal := validators[numVals-maxVals-1]
	require.Equal(t, cliffVal.Operator, sdk.AccAddress(keeper.GetCliffValidator(ctx)))

	// require the cliff validator power has changed
	cliffPower := keeper.GetCliffValidatorPower(ctx)
	require.Equal(t, GetValidatorsByPowerIndexKey(cliffVal, pool), cliffPower)

	expectedValStatus := map[int]sdk.BondStatus{
		9: sdk.Bonded, 8: sdk.Bonded, 7: sdk.Bonded, 5: sdk.Bonded, 4: sdk.Bonded,
		0: sdk.Unbonded, 1: sdk.Unbonded, 2: sdk.Unbonded, 3: sdk.Unbonded, 6: sdk.Unbonded,
	}

	// require all the validators have their respective statuses
	for valIdx, status := range expectedValStatus {
		valAddr := validators[valIdx].Operator
		val, _ := keeper.GetValidator(ctx, valAddr)

		require.Equal(
			t, val.GetStatus(), status,
			fmt.Sprintf("expected validator to have status: %s", sdk.BondStatusToString(status)))
	}
}

func TestCliffValidatorChange(t *testing.T) {
	numVals := 10
	maxVals := 5

	// create context, keeper, and pool for tests
	ctx, _, keeper := CreateTestInput(t, false, 0)
	pool := keeper.GetPool(ctx)

	// create keeper parameters
	params := keeper.GetParams(ctx)
	params.MaxValidators = uint16(maxVals)
	keeper.SetParams(ctx, params)

	// create a random pool
	pool.LooseTokens = sdk.NewDec(10000)
	pool.BondedTokens = sdk.NewDec(1234)
	keeper.SetPool(ctx, pool)

	validators := make([]types.Validator, numVals)
	for i := 0; i < len(validators); i++ {
		moniker := fmt.Sprintf("val#%d", int64(i))
		val := types.NewValidator(Addrs[i], PKs[i], types.Description{Moniker: moniker})
		val.BondHeight = int64(i)
		val.BondIntraTxCounter = int16(i)
		val, pool, _ = val.AddTokensFromDel(pool, sdk.NewInt(int64((i+1)*10)))

		keeper.SetPool(ctx, pool)
		val = keeper.UpdateValidator(ctx, val)
		validators[i] = val
	}

	// add a large amount of tokens to current cliff validator
	currCliffVal := validators[numVals-maxVals]
	currCliffVal, pool, _ = currCliffVal.AddTokensFromDel(pool, sdk.NewInt(200))
	keeper.SetPool(ctx, pool)
	currCliffVal = keeper.UpdateValidator(ctx, currCliffVal)

	// assert new cliff validator to be set to the second lowest bonded validator by power
	newCliffVal := validators[numVals-maxVals+1]
	require.Equal(t, newCliffVal.Operator, sdk.AccAddress(keeper.GetCliffValidator(ctx)))

	// assert cliff validator power should have been updated
	cliffPower := keeper.GetCliffValidatorPower(ctx)
	require.Equal(t, GetValidatorsByPowerIndexKey(newCliffVal, pool), cliffPower)

	// add small amount of tokens to new current cliff validator
	newCliffVal, pool, _ = newCliffVal.AddTokensFromDel(pool, sdk.NewInt(1))
	keeper.SetPool(ctx, pool)
	newCliffVal = keeper.UpdateValidator(ctx, newCliffVal)

	// assert cliff validator has not change but increased in power
	cliffPower = keeper.GetCliffValidatorPower(ctx)
	require.Equal(t, newCliffVal.Operator, sdk.AccAddress(keeper.GetCliffValidator(ctx)))
	require.Equal(t, GetValidatorsByPowerIndexKey(newCliffVal, pool), cliffPower)

	// add enough power to cliff validator to be equal in rank to next validator
	newCliffVal, pool, _ = newCliffVal.AddTokensFromDel(pool, sdk.NewInt(9))
	keeper.SetPool(ctx, pool)
	newCliffVal = keeper.UpdateValidator(ctx, newCliffVal)

	// assert new cliff validator due to power rank construction
	newCliffVal = validators[numVals-maxVals+2]
	require.Equal(t, newCliffVal.Operator, sdk.AccAddress(keeper.GetCliffValidator(ctx)))

	// assert cliff validator power should have been updated
	cliffPower = keeper.GetCliffValidatorPower(ctx)
	require.Equal(t, GetValidatorsByPowerIndexKey(newCliffVal, pool), cliffPower)
}

func TestSlashToZeroPowerRemoved(t *testing.T) {
	// initialize setup
	ctx, _, keeper := CreateTestInput(t, false, 100)
	pool := keeper.GetPool(ctx)

	// add a validator
	validator := types.NewValidator(addrVals[0], PKs[0], types.Description{})
	validator, pool, _ = validator.AddTokensFromDel(pool, sdk.NewInt(100))
	require.Equal(t, sdk.Unbonded, validator.Status)
	require.Equal(t, int64(100), validator.Tokens.RoundInt64())
	keeper.SetPool(ctx, pool)
	keeper.SetValidatorByPubKeyIndex(ctx, validator)
	validator = keeper.UpdateValidator(ctx, validator)
	require.Equal(t, int64(100), validator.Tokens.RoundInt64(), "\nvalidator %v\npool %v", validator, pool)

	// slash the validator by 100%
	keeper.Slash(ctx, PKs[0], 0, 100, sdk.OneDec())
	// validator should have been deleted
	_, found := keeper.GetValidator(ctx, addrVals[0])
	require.False(t, found)
}

// This function tests UpdateValidator, GetValidator, GetValidatorsBonded, RemoveValidator
func TestValidatorBasics(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)
	pool := keeper.GetPool(ctx)

	//construct the validators
	var validators [3]types.Validator
	amts := []int64{9, 8, 7}
	for i, amt := range amts {
		validators[i] = types.NewValidator(addrVals[i], PKs[i], types.Description{})
		validators[i].Status = sdk.Unbonded
		validators[i].Tokens = sdk.ZeroDec()
		validators[i], pool, _ = validators[i].AddTokensFromDel(pool, sdk.NewInt(amt))
		keeper.SetPool(ctx, pool)
	}
	assert.True(sdk.DecEq(t, sdk.NewDec(9), validators[0].Tokens))
	assert.True(sdk.DecEq(t, sdk.NewDec(8), validators[1].Tokens))
	assert.True(sdk.DecEq(t, sdk.NewDec(7), validators[2].Tokens))

	// check the empty keeper first
	_, found := keeper.GetValidator(ctx, addrVals[0])
	require.False(t, found)
	resVals := keeper.GetValidatorsBonded(ctx)
	assert.Zero(t, len(resVals))

	pool = keeper.GetPool(ctx)
	assert.True(sdk.DecEq(t, sdk.ZeroDec(), pool.BondedTokens))

	// set and retrieve a record
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	resVal, found := keeper.GetValidator(ctx, addrVals[0])
	require.True(t, found)
	assert.True(ValEq(t, validators[0], resVal))

	resVals = keeper.GetValidatorsBonded(ctx)
	require.Equal(t, 1, len(resVals))
	assert.True(ValEq(t, validators[0], resVals[0]))
	assert.Equal(t, sdk.Bonded, validators[0].Status)
	assert.True(sdk.DecEq(t, sdk.NewDec(9), validators[0].BondedTokens()))

	pool = keeper.GetPool(ctx)
	assert.True(sdk.DecEq(t, pool.BondedTokens, validators[0].BondedTokens()))

	// modify a records, save, and retrieve
	validators[0].Status = sdk.Bonded
	validators[0].Tokens = sdk.NewDec(10)
	validators[0].DelegatorShares = sdk.NewDec(10)
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	resVal, found = keeper.GetValidator(ctx, addrVals[0])
	require.True(t, found)
	assert.True(ValEq(t, validators[0], resVal))

	resVals = keeper.GetValidatorsBonded(ctx)
	require.Equal(t, 1, len(resVals))
	assert.True(ValEq(t, validators[0], resVals[0]))

	// add other validators
	validators[1] = keeper.UpdateValidator(ctx, validators[1])
	validators[2] = keeper.UpdateValidator(ctx, validators[2])
	resVal, found = keeper.GetValidator(ctx, addrVals[1])
	require.True(t, found)
	assert.True(ValEq(t, validators[1], resVal))
	resVal, found = keeper.GetValidator(ctx, addrVals[2])
	require.True(t, found)
	assert.True(ValEq(t, validators[2], resVal))

	resVals = keeper.GetValidatorsBonded(ctx)
	require.Equal(t, 3, len(resVals))
	assert.True(ValEq(t, validators[0], resVals[0])) // order doesn't matter here
	assert.True(ValEq(t, validators[1], resVals[1]))
	assert.True(ValEq(t, validators[2], resVals[2]))

	// remove a record
	keeper.RemoveValidator(ctx, validators[1].Operator)
	_, found = keeper.GetValidator(ctx, addrVals[1])
	require.False(t, found)
}

// test how the validators are sorted, tests GetValidatorsByPower
func GetValidatorSortingUnmixed(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)

	// initialize some validators into the state
	amts := []int64{0, 100, 1, 400, 200}
	n := len(amts)
	var validators [5]types.Validator
	for i, amt := range amts {
		validators[i] = types.NewValidator(Addrs[i], PKs[i], types.Description{})
		validators[i].Status = sdk.Bonded
		validators[i].Tokens = sdk.NewDec(amt)
		validators[i].DelegatorShares = sdk.NewDec(amt)
		keeper.UpdateValidator(ctx, validators[i])
	}

	// first make sure everything made it in to the gotValidator group
	resValidators := keeper.GetValidatorsByPower(ctx)
	assert.Equal(t, n, len(resValidators))
	assert.Equal(t, sdk.NewDec(400), resValidators[0].BondedTokens(), "%v", resValidators)
	assert.Equal(t, sdk.NewDec(200), resValidators[1].BondedTokens(), "%v", resValidators)
	assert.Equal(t, sdk.NewDec(100), resValidators[2].BondedTokens(), "%v", resValidators)
	assert.Equal(t, sdk.NewDec(1), resValidators[3].BondedTokens(), "%v", resValidators)
	assert.Equal(t, sdk.NewDec(0), resValidators[4].BondedTokens(), "%v", resValidators)
	assert.Equal(t, validators[3].Operator, resValidators[0].Operator, "%v", resValidators)
	assert.Equal(t, validators[4].Operator, resValidators[1].Operator, "%v", resValidators)
	assert.Equal(t, validators[1].Operator, resValidators[2].Operator, "%v", resValidators)
	assert.Equal(t, validators[2].Operator, resValidators[3].Operator, "%v", resValidators)
	assert.Equal(t, validators[0].Operator, resValidators[4].Operator, "%v", resValidators)

	// test a basic increase in voting power
	validators[3].Tokens = sdk.NewDec(500)
	keeper.UpdateValidator(ctx, validators[3])
	resValidators = keeper.GetValidatorsByPower(ctx)
	require.Equal(t, len(resValidators), n)
	assert.True(ValEq(t, validators[3], resValidators[0]))

	// test a decrease in voting power
	validators[3].Tokens = sdk.NewDec(300)
	keeper.UpdateValidator(ctx, validators[3])
	resValidators = keeper.GetValidatorsByPower(ctx)
	require.Equal(t, len(resValidators), n)
	assert.True(ValEq(t, validators[3], resValidators[0]))
	assert.True(ValEq(t, validators[4], resValidators[1]))

	// test equal voting power, different age
	validators[3].Tokens = sdk.NewDec(200)
	ctx = ctx.WithBlockHeight(10)
	keeper.UpdateValidator(ctx, validators[3])
	resValidators = keeper.GetValidatorsByPower(ctx)
	require.Equal(t, len(resValidators), n)
	assert.True(ValEq(t, validators[3], resValidators[0]))
	assert.True(ValEq(t, validators[4], resValidators[1]))
	require.Equal(t, int64(0), resValidators[0].BondHeight, "%v", resValidators)
	require.Equal(t, int64(0), resValidators[1].BondHeight, "%v", resValidators)

	// no change in voting power - no change in sort
	ctx = ctx.WithBlockHeight(20)
	keeper.UpdateValidator(ctx, validators[4])
	resValidators = keeper.GetValidatorsByPower(ctx)
	require.Equal(t, len(resValidators), n)
	assert.True(ValEq(t, validators[3], resValidators[0]))
	assert.True(ValEq(t, validators[4], resValidators[1]))

	// change in voting power of both validators, both still in v-set, no age change
	validators[3].Tokens = sdk.NewDec(300)
	validators[4].Tokens = sdk.NewDec(300)
	keeper.UpdateValidator(ctx, validators[3])
	resValidators = keeper.GetValidatorsByPower(ctx)
	require.Equal(t, len(resValidators), n)
	ctx = ctx.WithBlockHeight(30)
	keeper.UpdateValidator(ctx, validators[4])
	resValidators = keeper.GetValidatorsByPower(ctx)
	require.Equal(t, len(resValidators), n, "%v", resValidators)
	assert.True(ValEq(t, validators[3], resValidators[0]))
	assert.True(ValEq(t, validators[4], resValidators[1]))
}

func GetValidatorSortingMixed(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)

	// now 2 max resValidators
	params := keeper.GetParams(ctx)
	params.MaxValidators = 2
	keeper.SetParams(ctx, params)

	// initialize some validators into the state
	amts := []int64{0, 100, 1, 400, 200}

	n := len(amts)
	var validators [5]types.Validator
	for i, amt := range amts {
		validators[i] = types.NewValidator(Addrs[i], PKs[i], types.Description{})
		validators[i].DelegatorShares = sdk.NewDec(amt)
	}

	validators[0].Status = sdk.Bonded
	validators[1].Status = sdk.Bonded
	validators[2].Status = sdk.Bonded
	validators[0].Tokens = sdk.NewDec(amts[0])
	validators[1].Tokens = sdk.NewDec(amts[1])
	validators[2].Tokens = sdk.NewDec(amts[2])

	validators[3].Status = sdk.Bonded
	validators[4].Status = sdk.Bonded
	validators[3].Tokens = sdk.NewDec(amts[3])
	validators[4].Tokens = sdk.NewDec(amts[4])

	for i := range amts {
		keeper.UpdateValidator(ctx, validators[i])
	}
	val0, found := keeper.GetValidator(ctx, Addrs[0])
	require.True(t, found)
	val1, found := keeper.GetValidator(ctx, Addrs[1])
	require.True(t, found)
	val2, found := keeper.GetValidator(ctx, Addrs[2])
	require.True(t, found)
	val3, found := keeper.GetValidator(ctx, Addrs[3])
	require.True(t, found)
	val4, found := keeper.GetValidator(ctx, Addrs[4])
	require.True(t, found)
	require.Equal(t, sdk.Unbonded, val0.Status)
	require.Equal(t, sdk.Unbonded, val1.Status)
	require.Equal(t, sdk.Unbonded, val2.Status)
	require.Equal(t, sdk.Bonded, val3.Status)
	require.Equal(t, sdk.Bonded, val4.Status)

	// first make sure everything made it in to the gotValidator group
	resValidators := keeper.GetValidatorsByPower(ctx)
	assert.Equal(t, n, len(resValidators))
	assert.Equal(t, sdk.NewDec(400), resValidators[0].BondedTokens(), "%v", resValidators)
	assert.Equal(t, sdk.NewDec(200), resValidators[1].BondedTokens(), "%v", resValidators)
	assert.Equal(t, sdk.NewDec(100), resValidators[2].BondedTokens(), "%v", resValidators)
	assert.Equal(t, sdk.NewDec(1), resValidators[3].BondedTokens(), "%v", resValidators)
	assert.Equal(t, sdk.NewDec(0), resValidators[4].BondedTokens(), "%v", resValidators)
	assert.Equal(t, validators[3].Operator, resValidators[0].Operator, "%v", resValidators)
	assert.Equal(t, validators[4].Operator, resValidators[1].Operator, "%v", resValidators)
	assert.Equal(t, validators[1].Operator, resValidators[2].Operator, "%v", resValidators)
	assert.Equal(t, validators[2].Operator, resValidators[3].Operator, "%v", resValidators)
	assert.Equal(t, validators[0].Operator, resValidators[4].Operator, "%v", resValidators)
}

// TODO separate out into multiple tests
func TestGetValidatorsEdgeCases(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)
	var found bool

	// now 2 max resValidators
	params := keeper.GetParams(ctx)
	nMax := uint16(2)
	params.MaxValidators = nMax
	keeper.SetParams(ctx, params)

	// initialize some validators into the state
	amts := []int64{0, 100, 400, 400}
	var validators [4]types.Validator
	for i, amt := range amts {
		pool := keeper.GetPool(ctx)
		moniker := fmt.Sprintf("val#%d", int64(i))
		validators[i] = types.NewValidator(Addrs[i], PKs[i], types.Description{Moniker: moniker})
		validators[i], pool, _ = validators[i].AddTokensFromDel(pool, sdk.NewInt(amt))
		keeper.SetPool(ctx, pool)
		validators[i] = keeper.UpdateValidator(ctx, validators[i])
	}

	for i := range amts {
		validators[i], found = keeper.GetValidator(ctx, validators[i].Operator)
		require.True(t, found)
	}
	resValidators := keeper.GetValidatorsByPower(ctx)
	require.Equal(t, nMax, uint16(len(resValidators)))
	assert.True(ValEq(t, validators[2], resValidators[0]))
	assert.True(ValEq(t, validators[3], resValidators[1]))

	pool := keeper.GetPool(ctx)
	validators[0], pool, _ = validators[0].AddTokensFromDel(pool, sdk.NewInt(500))
	keeper.SetPool(ctx, pool)
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	resValidators = keeper.GetValidatorsByPower(ctx)
	require.Equal(t, nMax, uint16(len(resValidators)))
	assert.True(ValEq(t, validators[0], resValidators[0]))
	assert.True(ValEq(t, validators[2], resValidators[1]))

	// A validator which leaves the gotValidator set due to a decrease in voting power,
	// then increases to the original voting power, does not get its spot back in the
	// case of a tie.

	// validator 3 enters bonded validator set
	ctx = ctx.WithBlockHeight(40)

	validators[3], found = keeper.GetValidator(ctx, validators[3].Operator)
	require.True(t, found)
	validators[3], pool, _ = validators[3].AddTokensFromDel(pool, sdk.NewInt(1))
	keeper.SetPool(ctx, pool)
	validators[3] = keeper.UpdateValidator(ctx, validators[3])
	resValidators = keeper.GetValidatorsByPower(ctx)
	require.Equal(t, nMax, uint16(len(resValidators)))
	assert.True(ValEq(t, validators[0], resValidators[0]))
	assert.True(ValEq(t, validators[3], resValidators[1]))

	// validator 3 kicked out temporarily
	validators[3], pool, _ = validators[3].RemoveDelShares(pool, sdk.NewDec(201))
	keeper.SetPool(ctx, pool)
	validators[3] = keeper.UpdateValidator(ctx, validators[3])
	resValidators = keeper.GetValidatorsByPower(ctx)
	require.Equal(t, nMax, uint16(len(resValidators)))
	assert.True(ValEq(t, validators[0], resValidators[0]))
	assert.True(ValEq(t, validators[2], resValidators[1]))

	// validator 4 does not get spot back
	validators[3], pool, _ = validators[3].AddTokensFromDel(pool, sdk.NewInt(200))
	keeper.SetPool(ctx, pool)
	validators[3] = keeper.UpdateValidator(ctx, validators[3])
	resValidators = keeper.GetValidatorsByPower(ctx)
	require.Equal(t, nMax, uint16(len(resValidators)))
	assert.True(ValEq(t, validators[0], resValidators[0]))
	assert.True(ValEq(t, validators[2], resValidators[1]))
	validator, exists := keeper.GetValidator(ctx, validators[3].Operator)
	require.Equal(t, exists, true)
	require.Equal(t, int64(40), validator.BondHeight)
}

func TestValidatorBondHeight(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)
	pool := keeper.GetPool(ctx)

	// now 2 max resValidators
	params := keeper.GetParams(ctx)
	params.MaxValidators = 2
	keeper.SetParams(ctx, params)

	// initialize some validators into the state
	var validators [3]types.Validator
	validators[0] = types.NewValidator(Addrs[0], PKs[0], types.Description{})
	validators[1] = types.NewValidator(Addrs[1], PKs[1], types.Description{})
	validators[2] = types.NewValidator(Addrs[2], PKs[2], types.Description{})

	validators[0], pool, _ = validators[0].AddTokensFromDel(pool, sdk.NewInt(200))
	validators[1], pool, _ = validators[1].AddTokensFromDel(pool, sdk.NewInt(100))
	validators[2], pool, _ = validators[2].AddTokensFromDel(pool, sdk.NewInt(100))
	keeper.SetPool(ctx, pool)

	validators[0] = keeper.UpdateValidator(ctx, validators[0])

	////////////////////////////////////////
	// If two validators both increase to the same voting power in the same block,
	// the one with the first transaction should become bonded
	validators[1] = keeper.UpdateValidator(ctx, validators[1])
	validators[2] = keeper.UpdateValidator(ctx, validators[2])

	pool = keeper.GetPool(ctx)

	resValidators := keeper.GetValidatorsByPower(ctx)
	require.Equal(t, uint16(len(resValidators)), params.MaxValidators)

	assert.True(ValEq(t, validators[0], resValidators[0]))
	assert.True(ValEq(t, validators[1], resValidators[1]))
	validators[1], pool, _ = validators[1].AddTokensFromDel(pool, sdk.NewInt(50))
	validators[2], pool, _ = validators[2].AddTokensFromDel(pool, sdk.NewInt(50))
	keeper.SetPool(ctx, pool)
	validators[2] = keeper.UpdateValidator(ctx, validators[2])
	resValidators = keeper.GetValidatorsByPower(ctx)
	require.Equal(t, params.MaxValidators, uint16(len(resValidators)))
	validators[1] = keeper.UpdateValidator(ctx, validators[1])
	assert.True(ValEq(t, validators[0], resValidators[0]))
	assert.True(ValEq(t, validators[2], resValidators[1]))
}

func TestFullValidatorSetPowerChange(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)
	params := keeper.GetParams(ctx)
	max := 2
	params.MaxValidators = uint16(2)
	keeper.SetParams(ctx, params)

	// initialize some validators into the state
	amts := []int64{0, 100, 400, 400, 200}
	var validators [5]types.Validator
	for i, amt := range amts {
		pool := keeper.GetPool(ctx)
		validators[i] = types.NewValidator(Addrs[i], PKs[i], types.Description{})
		validators[i], pool, _ = validators[i].AddTokensFromDel(pool, sdk.NewInt(amt))
		keeper.SetPool(ctx, pool)
		keeper.UpdateValidator(ctx, validators[i])
	}
	for i := range amts {
		var found bool
		validators[i], found = keeper.GetValidator(ctx, validators[i].Operator)
		require.True(t, found)
	}
	assert.Equal(t, sdk.Unbonded, validators[0].Status)
	assert.Equal(t, sdk.Unbonded, validators[1].Status)
	assert.Equal(t, sdk.Bonded, validators[2].Status)
	assert.Equal(t, sdk.Bonded, validators[3].Status)
	assert.Equal(t, sdk.Unbonded, validators[4].Status)
	resValidators := keeper.GetValidatorsByPower(ctx)
	assert.Equal(t, max, len(resValidators))
	assert.True(ValEq(t, validators[2], resValidators[0])) // in the order of txs
	assert.True(ValEq(t, validators[3], resValidators[1]))

	// test a swap in voting power
	pool := keeper.GetPool(ctx)
	validators[0], pool, _ = validators[0].AddTokensFromDel(pool, sdk.NewInt(600))
	keeper.SetPool(ctx, pool)
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	resValidators = keeper.GetValidatorsByPower(ctx)
	assert.Equal(t, max, len(resValidators))
	assert.True(ValEq(t, validators[0], resValidators[0]))
	assert.True(ValEq(t, validators[2], resValidators[1]))
}

// clear the tracked changes to the gotValidator set
func TestClearTendermintUpdates(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)

	amts := []int64{100, 400, 200}
	validators := make([]types.Validator, len(amts))
	for i, amt := range amts {
		pool := keeper.GetPool(ctx)
		validators[i] = types.NewValidator(Addrs[i], PKs[i], types.Description{})
		validators[i], pool, _ = validators[i].AddTokensFromDel(pool, sdk.NewInt(amt))
		keeper.SetPool(ctx, pool)
		keeper.UpdateValidator(ctx, validators[i])
	}

	updates := keeper.GetTendermintUpdates(ctx)
	require.Equal(t, len(amts), len(updates))
	keeper.ClearTendermintUpdates(ctx)
	updates = keeper.GetTendermintUpdates(ctx)
	require.Equal(t, 0, len(updates))
}

func TestGetTendermintUpdatesAllNone(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)

	amts := []int64{10, 20}
	var validators [2]types.Validator
	for i, amt := range amts {
		pool := keeper.GetPool(ctx)
		validators[i] = types.NewValidator(Addrs[i], PKs[i], types.Description{})
		validators[i], pool, _ = validators[i].AddTokensFromDel(pool, sdk.NewInt(amt))
		keeper.SetPool(ctx, pool)
	}

	// test from nothing to something
	//  tendermintUpdate set: {} -> {c1, c3}
	require.Equal(t, 0, len(keeper.GetTendermintUpdates(ctx)))
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	validators[1] = keeper.UpdateValidator(ctx, validators[1])

	updates := keeper.GetTendermintUpdates(ctx)
	assert.Equal(t, 2, len(updates))
	assert.Equal(t, validators[0].ABCIValidator(), updates[0])
	assert.Equal(t, validators[1].ABCIValidator(), updates[1])

	// test from something to nothing
	//  tendermintUpdate set: {} -> {c1, c2, c3, c4}
	keeper.ClearTendermintUpdates(ctx)
	require.Equal(t, 0, len(keeper.GetTendermintUpdates(ctx)))

	keeper.RemoveValidator(ctx, validators[0].Operator)
	keeper.RemoveValidator(ctx, validators[1].Operator)

	updates = keeper.GetTendermintUpdates(ctx)
	assert.Equal(t, 2, len(updates))
	assert.Equal(t, tmtypes.TM2PB.PubKey(validators[0].PubKey), updates[0].PubKey)
	assert.Equal(t, tmtypes.TM2PB.PubKey(validators[1].PubKey), updates[1].PubKey)
	assert.Equal(t, int64(0), updates[0].Power)
	assert.Equal(t, int64(0), updates[1].Power)
}

func TestGetTendermintUpdatesIdentical(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)

	amts := []int64{10, 20}
	var validators [2]types.Validator
	for i, amt := range amts {
		pool := keeper.GetPool(ctx)
		validators[i] = types.NewValidator(Addrs[i], PKs[i], types.Description{})
		validators[i], pool, _ = validators[i].AddTokensFromDel(pool, sdk.NewInt(amt))
		keeper.SetPool(ctx, pool)
	}
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	validators[1] = keeper.UpdateValidator(ctx, validators[1])
	keeper.ClearTendermintUpdates(ctx)
	require.Equal(t, 0, len(keeper.GetTendermintUpdates(ctx)))

	// test identical,
	//  tendermintUpdate set: {} -> {}
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	validators[1] = keeper.UpdateValidator(ctx, validators[1])
	require.Equal(t, 0, len(keeper.GetTendermintUpdates(ctx)))
}

func TestGetTendermintUpdatesSingleValueChange(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)

	amts := []int64{10, 20}
	var validators [2]types.Validator
	for i, amt := range amts {
		pool := keeper.GetPool(ctx)
		validators[i] = types.NewValidator(Addrs[i], PKs[i], types.Description{})
		validators[i], pool, _ = validators[i].AddTokensFromDel(pool, sdk.NewInt(amt))
		keeper.SetPool(ctx, pool)
	}
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	validators[1] = keeper.UpdateValidator(ctx, validators[1])
	keeper.ClearTendermintUpdates(ctx)
	require.Equal(t, 0, len(keeper.GetTendermintUpdates(ctx)))

	// test single value change
	//  tendermintUpdate set: {} -> {c1'}
	validators[0].Status = sdk.Bonded
	validators[0].Tokens = sdk.NewDec(600)
	validators[0] = keeper.UpdateValidator(ctx, validators[0])

	updates := keeper.GetTendermintUpdates(ctx)

	require.Equal(t, 1, len(updates))
	require.Equal(t, validators[0].ABCIValidator(), updates[0])
}

func TestGetTendermintUpdatesMultipleValueChange(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)

	amts := []int64{10, 20}
	var validators [2]types.Validator
	for i, amt := range amts {
		pool := keeper.GetPool(ctx)
		validators[i] = types.NewValidator(Addrs[i], PKs[i], types.Description{})
		validators[i], pool, _ = validators[i].AddTokensFromDel(pool, sdk.NewInt(amt))
		keeper.SetPool(ctx, pool)
	}
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	validators[1] = keeper.UpdateValidator(ctx, validators[1])
	keeper.ClearTendermintUpdates(ctx)
	require.Equal(t, 0, len(keeper.GetTendermintUpdates(ctx)))

	// test multiple value change
	//  tendermintUpdate set: {c1, c3} -> {c1', c3'}
	pool := keeper.GetPool(ctx)
	validators[0], pool, _ = validators[0].AddTokensFromDel(pool, sdk.NewInt(190))
	validators[1], pool, _ = validators[1].AddTokensFromDel(pool, sdk.NewInt(80))
	keeper.SetPool(ctx, pool)
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	validators[1] = keeper.UpdateValidator(ctx, validators[1])

	updates := keeper.GetTendermintUpdates(ctx)
	require.Equal(t, 2, len(updates))
	require.Equal(t, validators[0].ABCIValidator(), updates[0])
	require.Equal(t, validators[1].ABCIValidator(), updates[1])
}

func TestGetTendermintUpdatesInserted(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)

	amts := []int64{10, 20, 5, 15, 25}
	var validators [5]types.Validator
	for i, amt := range amts {
		pool := keeper.GetPool(ctx)
		validators[i] = types.NewValidator(Addrs[i], PKs[i], types.Description{})
		validators[i], pool, _ = validators[i].AddTokensFromDel(pool, sdk.NewInt(amt))
		keeper.SetPool(ctx, pool)
	}
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	validators[1] = keeper.UpdateValidator(ctx, validators[1])
	keeper.ClearTendermintUpdates(ctx)
	require.Equal(t, 0, len(keeper.GetTendermintUpdates(ctx)))

	// test validtor added at the beginning
	//  tendermintUpdate set: {} -> {c0}
	validators[2] = keeper.UpdateValidator(ctx, validators[2])
	updates := keeper.GetTendermintUpdates(ctx)
	require.Equal(t, 1, len(updates))
	require.Equal(t, validators[2].ABCIValidator(), updates[0])

	// test validtor added at the beginning
	//  tendermintUpdate set: {} -> {c0}
	keeper.ClearTendermintUpdates(ctx)
	validators[3] = keeper.UpdateValidator(ctx, validators[3])
	updates = keeper.GetTendermintUpdates(ctx)
	require.Equal(t, 1, len(updates))
	require.Equal(t, validators[3].ABCIValidator(), updates[0])

	// test validtor added at the end
	//  tendermintUpdate set: {} -> {c0}
	keeper.ClearTendermintUpdates(ctx)
	validators[4] = keeper.UpdateValidator(ctx, validators[4])
	updates = keeper.GetTendermintUpdates(ctx)
	require.Equal(t, 1, len(updates))
	require.Equal(t, validators[4].ABCIValidator(), updates[0])
}

func TestGetTendermintUpdatesWithCliffValidator(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)
	params := types.DefaultParams()
	params.MaxValidators = 2
	keeper.SetParams(ctx, params)

	amts := []int64{10, 20, 5}
	var validators [5]types.Validator
	for i, amt := range amts {
		pool := keeper.GetPool(ctx)
		validators[i] = types.NewValidator(Addrs[i], PKs[i], types.Description{})
		validators[i], pool, _ = validators[i].AddTokensFromDel(pool, sdk.NewInt(amt))
		keeper.SetPool(ctx, pool)
	}
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	validators[1] = keeper.UpdateValidator(ctx, validators[1])
	keeper.ClearTendermintUpdates(ctx)
	require.Equal(t, 0, len(keeper.GetTendermintUpdates(ctx)))

	// test validator added at the end but not inserted in the valset
	//  tendermintUpdate set: {} -> {}
	keeper.UpdateValidator(ctx, validators[2])
	updates := keeper.GetTendermintUpdates(ctx)
	require.Equal(t, 0, len(updates))

	// test validator change its power and become a gotValidator (pushing out an existing)
	//  tendermintUpdate set: {}     -> {c0, c4}
	keeper.ClearTendermintUpdates(ctx)
	require.Equal(t, 0, len(keeper.GetTendermintUpdates(ctx)))

	pool := keeper.GetPool(ctx)
	validators[2], pool, _ = validators[2].AddTokensFromDel(pool, sdk.NewInt(10))
	keeper.SetPool(ctx, pool)
	validators[2] = keeper.UpdateValidator(ctx, validators[2])

	updates = keeper.GetTendermintUpdates(ctx)
	require.Equal(t, 2, len(updates), "%v", updates)
	require.Equal(t, validators[0].ABCIValidatorZero(), updates[0])
	require.Equal(t, validators[2].ABCIValidator(), updates[1])
}

func TestGetTendermintUpdatesPowerDecrease(t *testing.T) {
	ctx, _, keeper := CreateTestInput(t, false, 1000)

	amts := []int64{100, 100}
	var validators [2]types.Validator
	for i, amt := range amts {
		pool := keeper.GetPool(ctx)
		validators[i] = types.NewValidator(Addrs[i], PKs[i], types.Description{})
		validators[i], pool, _ = validators[i].AddTokensFromDel(pool, sdk.NewInt(amt))
		keeper.SetPool(ctx, pool)
	}
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	validators[1] = keeper.UpdateValidator(ctx, validators[1])
	keeper.ClearTendermintUpdates(ctx)
	require.Equal(t, 0, len(keeper.GetTendermintUpdates(ctx)))

	// check initial power
	require.Equal(t, sdk.NewDec(100).RoundInt64(), validators[0].GetPower().RoundInt64())
	require.Equal(t, sdk.NewDec(100).RoundInt64(), validators[1].GetPower().RoundInt64())

	// test multiple value change
	//  tendermintUpdate set: {c1, c3} -> {c1', c3'}
	pool := keeper.GetPool(ctx)
	validators[0], pool, _ = validators[0].RemoveDelShares(pool, sdk.NewDec(20))
	validators[1], pool, _ = validators[1].RemoveDelShares(pool, sdk.NewDec(30))
	keeper.SetPool(ctx, pool)
	validators[0] = keeper.UpdateValidator(ctx, validators[0])
	validators[1] = keeper.UpdateValidator(ctx, validators[1])

	// power has changed
	require.Equal(t, sdk.NewDec(80).RoundInt64(), validators[0].GetPower().RoundInt64())
	require.Equal(t, sdk.NewDec(70).RoundInt64(), validators[1].GetPower().RoundInt64())

	// Tendermint updates should reflect power change
	updates := keeper.GetTendermintUpdates(ctx)
	require.Equal(t, 2, len(updates))
	require.Equal(t, validators[0].ABCIValidator(), updates[0])
	require.Equal(t, validators[1].ABCIValidator(), updates[1])
}
