def execute(data):
    credit, used = 0, set()
    for _ in range(data["deliveries"]):
        if data["nonce"] not in used:
            credit += data["amount"]
    return {"credit": credit}
