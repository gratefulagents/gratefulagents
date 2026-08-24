def execute(data):
    total = sum(data["numerators"])
    d = data["denominator"]
    return {"payout": (total + d - 1) // d}
