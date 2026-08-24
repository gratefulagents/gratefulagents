def execute(data):
    credit, used = 0, set()
    for _ in range(data["deliveries"]):
        if data["nonce"] not in used:
            used.add(data["nonce"]); credit += data["amount"]
    return {"credit": credit}
