def execute(data):
    d = data["denominator"]
    payout = sum((n + d - 1) // d for n in data["numerators"] )
    return {"payout": payout}
